//go:build darwin

package procattr

import (
	"bytes"
	"fmt"

	"golang.org/x/sys/unix"
)

// procLookup 用 sysctl 逐个查进程，不起子进程（hook 每次工具调用前后各跑一次，要快）。
func procLookup() (func(int) (Ancestor, error), error) { return lookupProc, nil }

func lookupProc(pid int) (Ancestor, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Ancestor{}, fmt.Errorf("查进程 %d：%w", pid, err)
	}
	if int(k.Proc.P_pid) != pid {
		return Ancestor{}, fmt.Errorf("进程 %d 不在了", pid)
	}
	a := Ancestor{PID: pid, PPID: int(k.Eproc.Ppid)}
	// kern.procargs2：argc（4 字节）后面是可执行文件路径（NUL 结尾）；别的用户的进程读不到，退回 16 字节的短名
	if raw, err := unix.SysctlRaw("kern.procargs2", pid); err == nil && len(raw) > 4 {
		p := raw[4:]
		if i := bytes.IndexByte(p, 0); i > 0 {
			a.Path = string(p[:i])
		}
	}
	if a.Path == "" {
		c := k.Proc.P_comm[:]
		if i := bytes.IndexByte(c, 0); i >= 0 {
			c = c[:i]
		}
		a.Path = string(c)
	}
	return a, nil
}
