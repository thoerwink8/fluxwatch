//go:build windows

package mihomo

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// autoLocate 列出 \\.\pipe\ 下名字带 mihomo 的命名管道，取第一个 /version 能答上的。
// Windows 上 Clash Party 的接口形态没在真机验证过；找不到就要求在设置里手填 TCP 控制器。
func autoLocate(ctx context.Context) (Endpoint, error) {
	names, err := listPipes()
	if err != nil {
		return Endpoint{}, fmt.Errorf("%w：列不出命名管道（%v），请在设置里手填 external-controller 地址和 secret", ErrNotFound, err)
	}
	var errs []string
	for _, n := range names {
		if !strings.Contains(strings.ToLower(n), "mihomo") {
			continue
		}
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
		return Endpoint{}, fmt.Errorf("%w：没有名字带 mihomo 的命名管道，请在设置里手填 external-controller 地址和 secret", ErrNotFound)
	}
	return Endpoint{}, fmt.Errorf("%w：命名管道都连不上（%s），请在设置里手填 external-controller", ErrNotFound, strings.Join(errs, "；"))
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
