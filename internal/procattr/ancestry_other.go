//go:build !darwin && !windows

package procattr

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procLookup 读 /proc（Linux）；没有 /proc 的平台查不到，返回的错误由 Ancestry 照实交出去。
func procLookup() (func(int) (Ancestor, error), error) { return lookupProc, nil }

func lookupProc(pid int) (Ancestor, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	stat, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return Ancestor{}, fmt.Errorf("查进程 %d：%w", pid, err)
	}
	// 形如「123 (comm) S 1 ...」：comm 里可能有空格和括号，从最后一个「)」往后切
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return Ancestor{}, fmt.Errorf("进程 %d 的 stat 看不懂", pid)
	}
	f := strings.Fields(string(stat[i+1:]))
	if len(f) < 2 {
		return Ancestor{}, fmt.Errorf("进程 %d 的 stat 看不懂", pid)
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return Ancestor{}, fmt.Errorf("进程 %d 的 ppid 看不懂", pid)
	}
	a := Ancestor{PID: pid, PPID: ppid}
	// argv[0] 保留经符号链接启动时的名字（/proc/<pid>/exe 会解析到真文件）
	if cl, err := os.ReadFile(dir + "/cmdline"); err == nil {
		if j := bytes.IndexByte(cl, 0); j > 0 {
			a.Path = string(cl[:j])
		}
	}
	if a.Path == "" {
		if s := bytes.IndexByte(stat, '('); s >= 0 && s < i {
			a.Path = string(stat[s+1 : i])
		}
	}
	return a, nil
}
