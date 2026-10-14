// Package procattr 把本机连接的源端口反查到 PID，再上溯到软件名。
//
// mac 用 lsof + ps（上溯 ppid 到 .app），Windows 用 GetExtendedTcpTable + 进程映像路径，
// 其他平台不支持（返回 ErrUnsupported）。结果按端口缓存：本轮请求里没有的端口就清掉，只有出现新端口时才跑一次反查（mac 上是 lsof）。
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

// maxMiss 是一个端口反查不到时最多再试几次（连接刚建立，表里可能还没有），之后按「没有进程」缓存。
const maxMiss = 3

type portEntry struct {
	proc  Proc
	found bool
	tries int // 反查不到的次数
}

// Attributor 按端口缓存的反查器，可并发用。
type Attributor struct {
	mu    sync.Mutex
	plat  Platform
	ports map[int]*portEntry
	names map[int]string // PID → 软件名，只保留还有端口引用的 PID
}

// New 用本平台的实现。
func New() *Attributor { return NewWith(platform()) }

// NewWith 用给定的实现。
func NewWith(p Platform) *Attributor {
	return &Attributor{plat: p, ports: map[int]*portEntry{}, names: map[int]string{}}
}

// Lookup 把一批源端口反查到进程。反查失败返回错误，调用方照实报，不当成「没有进程」。
// 本轮请求里没有的端口从缓存清掉；请求的端口都已缓存时不碰平台（不起 lsof）。
func (a *Attributor) Lookup(ports []int) (map[int]Proc, error) {
	if a == nil || a.plat == nil {
		return nil, ErrUnsupported
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	want := map[int]bool{}
	for _, p := range ports {
		want[p] = true
	}
	for p := range a.ports {
		if !want[p] {
			delete(a.ports, p)
		}
	}
	var fresh []int
	for p := range want {
		if e, ok := a.ports[p]; !ok || (!e.found && e.tries < maxMiss) {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) > 0 {
		table, err := a.plat.PortPIDs()
		if err != nil {
			return nil, err
		}
		var miss []int
		seen := map[int]bool{}
		for _, port := range fresh {
			e := a.ports[port]
			if e == nil {
				e = &portEntry{}
				a.ports[port] = e
			}
			pid, ok := table[port]
			if !ok || pid <= 0 {
				e.tries++
				continue
			}
			e.found, e.proc = true, Proc{PID: pid}
			if _, ok := a.names[pid]; !ok && !seen[pid] {
				miss = append(miss, pid)
				seen[pid] = true
			}
		}
		if len(miss) > 0 {
			got := a.plat.AppNames(miss)
			for _, pid := range miss {
				a.names[pid] = got[pid] // 拿不到也缓存空串，免得重试
			}
		}
	}
	used := map[int]bool{}
	out := map[int]Proc{}
	for port, e := range a.ports {
		if e.found {
			used[e.proc.PID] = true
			out[port] = Proc{PID: e.proc.PID, App: a.names[e.proc.PID]}
		}
	}
	for pid := range a.names {
		if !used[pid] {
			delete(a.names, pid)
		}
	}
	return out, nil
}
