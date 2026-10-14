package mihomo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 自动探测的顺序：先找本机 IPC（socket / 命名管道，免密钥，平台相关），都没有再试常见的 TCP 控制器。
//
// Clash Verge Rev 的位置照它的源码（clash-verge-rev src-tauri/src/utils/dirs.rs、
// clash-verge-service-ipc src/core/paths.rs v2.7.6）：
//   - mac sidecar：用户临时目录（$TMPDIR，confstr _CS_DARWIN_USER_TEMP_DIR）下的 verge-mihomo.sock
//   - mac 服务模式：/var/run/clash-verge-service/users/<uid>/verge-mihomo.sock（Linux 是 /run/...）
//   - v2.4 及以前：/var/tmp/verge/verge-mihomo.sock（/var/tmp 不在时 /tmp/verge/）
//   - Windows：\\.\pipe\verge-mihomo-sidecar-release-<owner>、服务模式 \\.\pipe\verge-mihomo-<channel>-<owner>、
//     v2.4 及以前 \\.\pipe\verge-mihomo
//   - TCP：external-controller 默认 127.0.0.1:9097，secret 默认 set-your-secret（config/clash.rs）

// TCPCandidates 是最后试的 TCP 控制器：9090 是 mihomo / Clash 系的常见默认，9097 是 Clash Verge Rev 的默认。
var TCPCandidates = []string{"127.0.0.1:9090", "127.0.0.1:9097"}

// ErrUnauthorized 表示控制器要 secret，或者给的 secret 不对。
var ErrUnauthorized = errors.New("secret 不对")

// autoLocate 先找 IPC，再试 TCP；secret 只给 TCP 用（IPC 免密钥）。
func autoLocate(ctx context.Context, secret string) (Endpoint, error) {
	ep, ipcErr := locateIPC(ctx)
	if ipcErr == nil {
		return ep, nil
	}
	ep, tcpErr := locateTCP(ctx, TCPCandidates, secret)
	if tcpErr == nil {
		return ep, nil
	}
	return Endpoint{}, fmt.Errorf("%w：%v；%v", ErrNotFound, ipcErr, tcpErr)
}

// locateTCP 依次试 addrs，取第一个 /version 能答上的。连不上的照实合成一句话；
// 有 mihomo 但要 secret 的单独说清怎么办。
func locateTCP(ctx context.Context, addrs []string, secret string) (Endpoint, error) {
	var locked []string
	for _, a := range addrs {
		ep := Endpoint{Kind: KindTCP, Addr: a, Secret: secret}
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := New(ep).Version(pctx)
		cancel()
		if err == nil {
			return ep, nil
		}
		if errors.Is(err, ErrUnauthorized) {
			locked = append(locked, a)
		}
	}
	if len(locked) > 0 {
		return Endpoint{}, fmt.Errorf("%s 上有控制器但要 secret：在设置里选「手填」，填这个地址和 Clash 里设的 secret",
			strings.Join(locked, "、"))
	}
	return Endpoint{}, fmt.Errorf("%s 上也没有（Clash 没开？控制器在别的地址就在设置里手填）", strings.Join(addrs, "、"))
}

// mihomoPipes 从命名管道列表里挑出 mihomo 的控制接口：名字带 mihomo（Clash Party、Clash Verge Rev 都是），
// 不分大小写。Clash Verge 的服务控制管道 clash-verge-service 不是 mihomo，不要。
func mihomoPipes(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), "mihomo") {
			out = append(out, n)
		}
	}
	return out
}
