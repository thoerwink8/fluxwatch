//go:build windows

package procattr

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procLookup 拍一次进程快照（pid → 父 pid、映像文件名），上溯时都查这一份。
// Windows 的父 pid 在父进程退出后可能被复用；hook 上溯时父进程（claude）一定还活着，往上几层错认的概率可以接受。
func procLookup() (func(int) (Ancestor, error), error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("进程快照：%w", err)
	}
	defer windows.CloseHandle(snap)
	table := map[int]Ancestor{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		pid := int(e.ProcessID)
		table[pid] = Ancestor{PID: pid, PPID: int(e.ParentProcessID), Path: windows.UTF16ToString(e.ExeFile[:])}
	}
	return func(pid int) (Ancestor, error) {
		a, ok := table[pid]
		if !ok {
			return Ancestor{}, fmt.Errorf("进程 %d 不在快照里", pid)
		}
		return a, nil
	}, nil
}
