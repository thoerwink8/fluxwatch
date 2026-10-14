//go:build !windows

package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SocketGlobs 是 mac / Linux 上找 socket 的位置（glob），出处见 locate.go 顶上的注释：
//   - Clash Party：/tmp/mihomo-party-<uid>-<pid>.sock，核心重启 pid 会变
//   - Clash Verge Rev ≥ 2.5：sidecar 在用户临时目录（mac 的 $TMPDIR；Linux 在数据目录），服务模式在 clash-verge-service/users/<uid>/ 下
//   - Clash Verge Rev ≤ 2.4：/var/tmp/verge/、/tmp/verge/
var SocketGlobs = socketGlobs()

func socketGlobs() []string {
	uid := strconv.Itoa(os.Getuid())
	gs := []string{
		"/tmp/mihomo-party-*.sock",
		filepath.Join(os.TempDir(), "verge-mihomo*.sock"),
		"/var/run/clash-verge-service/users/" + uid + "/verge-mihomo*.sock",
		"/run/clash-verge-service/users/" + uid + "/verge-mihomo*.sock",
		"/var/tmp/verge/verge-mihomo*.sock",
		"/tmp/verge/verge-mihomo*.sock",
	}
	if runtime.GOOS == "linux" {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			if h, err := os.UserHomeDir(); err == nil {
				data = filepath.Join(h, ".local", "share")
			}
		}
		if data != "" {
			gs = append(gs, filepath.Join(data, "io.github.clash-verge-rev.clash-verge-rev", "verge-mihomo*.sock"))
		}
	}
	return gs
}

// locateIPC 按 glob 找 socket，新的在前，取第一个 /version 能答上的。
// 同目录还有 mihomo-party-helper.sock 一类不是控制接口的 socket，靠 /version 筛掉。
func locateIPC(ctx context.Context) (Endpoint, error) {
	return locateSockets(ctx, SocketGlobs...)
}

func locateSockets(ctx context.Context, globs ...string) (Endpoint, error) {
	seen := map[string]bool{}
	var paths []string
	for _, g := range globs {
		ps, err := filepath.Glob(g) // 只在 pattern 写坏时报错；目录不存在、没权限都只是没匹配
		if err != nil {
			return Endpoint{}, err
		}
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	type cand struct {
		path string
		mod  time.Time
	}
	var cs []cand
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		cs = append(cs, cand{p, fi.ModTime()})
	}
	if len(cs) == 0 {
		return Endpoint{}, errors.New("没有 Clash Party / Clash Verge 的 socket")
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].mod.After(cs[j].mod) })
	var errs []string
	for _, c := range cs {
		ep := Endpoint{Kind: KindUnix, Addr: c.path}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := New(ep).Version(pctx)
		cancel()
		if err == nil {
			return ep, nil
		}
		errs = append(errs, filepath.Base(c.path)+"："+err.Error())
	}
	return Endpoint{}, fmt.Errorf("socket 都连不上（%s）", strings.Join(errs, "；"))
}

func dialPipe(context.Context, string) (net.Conn, error) {
	return nil, errors.New("命名管道只在 Windows 上有")
}
