//go:build !windows

package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func serveUnix(t *testing.T, path string, h http.Handler) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

func TestLocateSocketSkipsNonMihomoAndReadsChunked(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fw") // unix socket 路径有长度上限，不用 t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// helper socket：不是控制接口，/version 404
	serveUnix(t, filepath.Join(dir, "mihomo-party-helper.sock"), http.NotFoundHandler())
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"version":"v1.19.14"}`) })
	mux.HandleFunc("/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"downloadTotal":1,"connections":[{"id":"a","metadata":{"host":"api.example.com","sourcePort":"5000"},`))
		w.(http.Flusher).Flush()
		w.Write([]byte(`"upload":10,"download":20,"chains":["JP-1","Proxy"]}]}`))
	})
	serveUnix(t, filepath.Join(dir, "mihomo-party-501-42.sock"), mux)
	// helper 更新：按新旧排序后它在前，也要被 /version 筛掉
	future := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(dir, "mihomo-party-helper.sock"), future, future)

	ep, err := locateSockets(context.Background(), filepath.Join(dir, "mihomo-party-*.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if ep.Kind != KindUnix || filepath.Base(ep.Addr) != "mihomo-party-501-42.sock" {
		t.Fatalf("找到 %v", ep)
	}
	cs, err := New(ep).Connections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Connections) != 1 || cs.Connections[0].Node() != "JP-1" || cs.Connections[0].HostName() != "api.example.com" || cs.Connections[0].Download != 20 {
		t.Fatalf("连接 = %+v", cs.Connections)
	}
}

func TestLocateNothingIsExplicitError(t *testing.T) {
	_, err := locateSockets(context.Background(), filepath.Join(t.TempDir(), "mihomo-party-*.sock"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
