// Package procattr 把本机连接的源端口反查到 PID，再上溯到软件名。
//
// mac 用 lsof + ps（上溯 ppid 到 .app），Windows 用 GetExtendedTcpTable + 进程映像路径，
// 其他平台不支持（返回 ErrUnsupported）。软件名按 PID 缓存，进程不在表里了就清掉。
package procattr

import (
	"errors"
	"sync"
)

// Proc 是一个端口反查到的进程。
type Proc struct {
	PID int
	App string // 软件名；拿不到为空
}

// ErrUnsupported 表示这个平台没有反查实现。
var ErrUnsupported = errors.New("这个平台不支持端口反查进程")

// Platform 是平台相关的两步：本机 TCP 端口 → PID，PID → 软件名。测试可以换成假的。
type Platform interface {
	PortPIDs() (map[int]int, error)     // 本地端口 → PID（只要 ESTABLISHED 的）
	AppNames(pids []int) map[int]string // PID → 软件名；拿不到的不放
}

// Attributor 带 PID 缓存的反查器，可并发用。
type Attributor struct {
	mu    sync.Mutex
	plat  Platform
	cache map[int]string
}

// New 用本平台的实现。
func New() *Attributor { return NewWith(platform()) }

// NewWith 用给定的实现。
func NewWith(p Platform) *Attributor { return &Attributor{plat: p, cache: map[int]string{}} }

// Lookup 把一批源端口反查到进程。反查失败返回错误，调用方照实报，不当成「没有进程」。
func (a *Attributor) Lookup(ports []int) (map[int]Proc, error) {
	if a == nil || a.plat == nil {
		return nil, ErrUnsupported
	}
	table, err := a.plat.PortPIDs()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	live := map[int]bool{}
	for _, pid := range table {
		live[pid] = true
	}
	for pid := range a.cache {
		if !live[pid] {
			delete(a.cache, pid)
		}
	}
	out := map[int]Proc{}
	var miss []int
	seen := map[int]bool{}
	for _, port := range ports {
		pid, ok := table[port]
		if !ok || pid <= 0 {
			continue
		}
		out[port] = Proc{PID: pid}
		if _, ok := a.cache[pid]; !ok && !seen[pid] {
			miss = append(miss, pid)
			seen[pid] = true
		}
	}
	if len(miss) > 0 {
		names := a.plat.AppNames(miss)
		for _, pid := range miss {
			a.cache[pid] = names[pid] // 拿不到也缓存空串，免得每秒重试
		}
	}
	for port, p := range out {
		p.App = a.cache[p.PID]
		out[port] = p
	}
	return out, nil
}
