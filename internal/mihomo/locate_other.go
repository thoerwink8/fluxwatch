//go:build !windows

package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SocketGlob 是 Clash Party 的 socket 位置：/tmp/mihomo-party-<uid>-<pid>.sock，核心重启 pid 会变。
var SocketGlob = "/tmp/mihomo-party-*.sock"

// autoLocate 按 glob 找 socket，新的在前，取第一个 /version 能答上的。
// 同目录还有 mihomo-party-helper.sock 一类不是控制接口的 socket，靠 /version 筛掉。
func autoLocate(ctx context.Context) (Endpoint, error) {
	return locateSockets(ctx, SocketGlob)
}

func locateSockets(ctx context.Context, glob string) (Endpoint, error) {
	paths, err := filepath.Glob(glob)
	if err != nil {
		return Endpoint{}, err
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
		return Endpoint{}, fmt.Errorf("%w：%s 没有匹配的 socket（Clash Party 没开？或在设置里手填 external-controller）", ErrNotFound, glob)
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
	return Endpoint{}, fmt.Errorf("%w：socket 都连不上（%s）", ErrNotFound, strings.Join(errs, "；"))
}

func dialPipe(context.Context, string) (net.Conn, error) {
	return nil, errors.New("命名管道只在 Windows 上有")
}
