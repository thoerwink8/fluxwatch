package mihomo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManualTCPWithSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"version":"v1.19.14"}`)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if _, err := Locate(context.Background(), Manual{Addr: addr, Secret: "s3"}); err != nil {
		t.Fatal(err)
	}
	_, err := Locate(context.Background(), Manual{Addr: srv.URL, Secret: "wrong"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("secret 不对应当明说：%v", err)
	}
}
