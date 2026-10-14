//go:build !windows

package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// 没 socket、TCP 候选也没人听：ErrNotFound，并说清两样都试过
	oldG, oldT := SocketGlobs, TCPCandidates
	t.Cleanup(func() { SocketGlobs, TCPCandidates = oldG, oldT })
	SocketGlobs = []string{filepath.Join(t.TempDir(), "mihomo-party-*.sock")}
	TCPCandidates = []string{deadAddr(t)}
	_, err := autoLocate(context.Background(), "")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "Clash Verge") || !strings.Contains(err.Error(), TCPCandidates[0]) {
		t.Fatalf("err = %v", err)
	}
}

// Clash Verge Rev ≥ 2.5 的 sidecar socket 在 $TMPDIR/verge-mihomo.sock，服务模式在 clash-verge-service/users/<uid>/ 下；
// 和 Clash Party 的一起按 glob 找，新的在前。
func TestSocketGlobsCoverClashVerge(t *testing.T) {
	gs := strings.Join(socketGlobs(), "\n")
	uid := strconv.Itoa(os.Getuid())
	for _, want := range []string{
		"/tmp/mihomo-party-*.sock",
		filepath.Join(os.TempDir(), "verge-mihomo*.sock"),
		"/var/run/clash-verge-service/users/" + uid + "/verge-mihomo*.sock",
		"/var/tmp/verge/verge-mihomo*.sock",
		"/tmp/verge/verge-mihomo*.sock",
	} {
		if !strings.Contains(gs, want) {
			t.Errorf("没找 %s：\n%s", want, gs)
		}
	}
}

func TestLocateClashVergeSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	verge := filepath.Join(dir, "verge", "verge-mihomo.sock")
	if err := os.MkdirAll(filepath.Dir(verge), 0o700); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"version":"v1.19.15"}`) })
	serveUnix(t, verge, mux)

	oldG, oldT := SocketGlobs, TCPCandidates
	t.Cleanup(func() { SocketGlobs, TCPCandidates = oldG, oldT })
	SocketGlobs = []string{filepath.Join(dir, "mihomo-party-*.sock"), filepath.Join(dir, "verge", "verge-mihomo*.sock")}
	TCPCandidates = []string{deadAddr(t)}
	ep, err := autoLocate(context.Background(), "")
	if err != nil || ep.Kind != KindUnix || ep.Addr != verge {
		t.Fatalf("ep = %v err = %v", ep, err)
	}
}

// 没有 socket 时退到 TCP 候选：第一个没人听，第二个要 secret，带对 secret 能用；不带 secret 时说清要去设置里填。
func TestLocateFallsBackToTCPWithSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer set-your-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"version":"v1.19.15"}`)
	}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")

	oldG, oldT := SocketGlobs, TCPCandidates
	t.Cleanup(func() { SocketGlobs, TCPCandidates = oldG, oldT })
	SocketGlobs = []string{filepath.Join(t.TempDir(), "none-*.sock")}
	TCPCandidates = []string{deadAddr(t), addr}

	ep, err := Locate(context.Background(), Manual{Secret: "set-your-secret"})
	if err != nil || ep.Kind != KindTCP || ep.Addr != addr || ep.Secret != "set-your-secret" {
		t.Fatalf("ep = %v err = %v", ep, err)
	}
	_, err = Locate(context.Background(), Manual{})
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), addr+" 上有控制器但要 secret") || !strings.Contains(err.Error(), "手填") {
		t.Fatalf("要 secret 时应当说清怎么办：%v", err)
	}
}

// deadAddr 返回一个此刻没人听的本机端口。
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}
