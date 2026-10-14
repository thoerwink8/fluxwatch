package procattr

import (
	"errors"
	"testing"
)

func TestParseLsof(t *testing.T) {
	out := "p691\ncmariadbd\nf58\nn127.0.0.1:33174->127.0.0.1:60094\np5623\ncMirasim Helper\nf25\nn127.0.0.1:65356->127.0.0.1:7890\nf26\nn[::1]:65357->[::1]:7890\np107\ncmihomo\nf9\nn127.0.0.1:7890->127.0.0.1:65356\n"
	m := parseLsof(out)
	if m[65356] != 5623 || m[65357] != 5623 || m[33174] != 691 || m[7890] != 107 {
		t.Fatalf("parseLsof = %v", m)
	}
}

func TestAppFromTree(t *testing.T) {
	ps := parsePs(`    1     0 /sbin/launchd
  500     1 /Applications/Mirasim.app/Contents/MacOS/Mirasim
  510   500 /Applications/Mirasim.app/Contents/Frameworks/Mirasim Helper.app/Contents/MacOS/Mirasim Helper
  520   500 /usr/local/bin/node
  530   520 /bin/sh
  540   530 /usr/local/bin/claude
  600     1 /System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal
  610   600 /bin/zsh
  620   610 /usr/local/bin/claude
  700     1 /usr/local/bin/curl
`)
	cases := map[int]string{
		510: "Mirasim", // helper 本身在 .app 里，取最外层
		540: "Mirasim", // 上溯 3 层到 Mirasim
		620: "claude",  // 上溯到终端：报命令本身
		700: "curl",
		999: "",
	}
	for pid, want := range cases {
		if got := appFromTree(pid, ps); got != want {
			t.Errorf("pid %d → %q，要 %q", pid, got, want)
		}
	}
}

type fakePlat struct {
	table map[int]int
	err   error
	calls int
}

func (f *fakePlat) PortPIDs() (map[int]int, error) { return f.table, f.err }
func (f *fakePlat) AppNames(pids []int) map[int]string {
	f.calls++
	m := map[int]string{}
	for _, p := range pids {
		m[p] = "App"
	}
	return m
}

func TestAttributorCachesByPIDAndEvicts(t *testing.T) {
	fp := &fakePlat{table: map[int]int{5000: 42, 5001: 42}}
	a := NewWith(fp)
	m, err := a.Lookup([]int{5000, 5001, 6000})
	if err != nil || m[5000].App != "App" || m[5001].PID != 42 {
		t.Fatalf("lookup = %v %v", m, err)
	}
	if _, ok := m[6000]; ok {
		t.Fatal("查不到的端口不该有结果")
	}
	a.Lookup([]int{5000})
	if fp.calls != 1 {
		t.Fatalf("同一 PID 应当走缓存，查了 %d 次", fp.calls)
	}
	fp.table = map[int]int{}
	a.Lookup(nil)
	if len(a.cache) != 0 {
		t.Fatal("进程没了应当清缓存")
	}
	fp.err = errors.New("lsof 失败")
	if _, err := a.Lookup([]int{1}); err == nil {
		t.Fatal("反查失败要返回错误")
	}
}
