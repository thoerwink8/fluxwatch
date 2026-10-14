//go:build windows

package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// locateIPC 列出 \\.\pipe\ 下名字带 mihomo 的命名管道（Clash Party；Clash Verge Rev 的 verge-mihomo、
// verge-mihomo-sidecar-release-<owner>、verge-mihomo-<channel>-<owner>），取第一个 /version 能答上的。
// Windows 上的接口形态没在真机验证过；都不行时 autoLocate 接着试 TCP。
func locateIPC(ctx context.Context) (Endpoint, error) {
	names, err := listPipes()
	if err != nil {
		return Endpoint{}, fmt.Errorf("列不出命名管道（%v）", err)
	}
	var errs []string
	for _, n := range mihomoPipes(names) {
		ep := Endpoint{Kind: KindPipe, Addr: `\\.\pipe\` + n}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := New(ep).Version(pctx)
		cancel()
		if err == nil {
			return ep, nil
		}
		errs = append(errs, n+"："+err.Error())
	}
	if len(errs) == 0 {
		return Endpoint{}, errors.New("没有 Clash Party / Clash Verge 的命名管道（名字带 mihomo）")
	}
	return Endpoint{}, fmt.Errorf("命名管道都连不上（%s）", strings.Join(errs, "；"))
}

func listPipes() ([]string, error) {
	var fd windows.Win32finddata
	pattern, err := windows.UTF16PtrFromString(`\\.\pipe\*`)
	if err != nil {
		return nil, err
	}
	h, err := windows.FindFirstFile(pattern, &fd)
	if err != nil {
		return nil, err
	}
	defer windows.FindClose(h)
	var out []string
	for {
		out = append(out, windows.UTF16ToString(fd.FileName[:]))
		if err := windows.FindNextFile(h, &fd); err != nil {
			break
		}
	}
	return out, nil
}

func dialPipe(ctx context.Context, name string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, name)
}
