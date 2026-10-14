package procattr

import (
	"errors"
	"os"
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
	calls int // AppNames 次数
	scans int // PortPIDs 次数（mac 上等于 lsof 次数）
}

func (f *fakePlat) PortPIDs() (map[int]int, error) { f.scans++; return f.table, f.err }
func (f *fakePlat) AppNames(pids []int) map[int]string {
	f.calls++
	m := map[int]string{}
	for _, p := range pids {
		m[p] = "App"
	}
	return m
}

func TestAttributorCachesByPortAndEvicts(t *testing.T) {
	fp := &fakePlat{table: map[int]int{5000: 42, 5001: 42}}
	a := NewWith(fp)
	m, err := a.Lookup([]int{5000, 5001})
	if err != nil || m[5000].App != "App" || m[5001].PID != 42 {
		t.Fatalf("lookup = %v %v", m, err)
	}
	a.Lookup([]int{5000})
	if fp.calls != 1 {
		t.Fatalf("同一 PID 应当走缓存，查了 %d 次", fp.calls)
	}
	a.Lookup(nil)
	if len(a.ports) != 0 || len(a.names) != 0 {
		t.Fatal("本轮没有的端口应当清缓存")
	}
	fp.err = errors.New("lsof 失败")
	if _, err := a.Lookup([]int{1}); err == nil {
		t.Fatal("反查失败要返回错误")
	}
}

func TestAttributorOnlyScansOnNewPorts(t *testing.T) {
	fp := &fakePlat{table: map[int]int{5000: 42, 5001: 43}}
	a := NewWith(fp)
	for i := 0; i < 10; i++ {
		m, _ := a.Lookup([]int{5000, 5001})
		if m[5000].PID != 42 || m[5001].PID != 43 {
			t.Fatalf("第 %d 轮结果不对：%v", i, m)
		}
	}
	if fp.scans != 1 {
		t.Fatalf("没有新端口不该再反查，扫了 %d 次", fp.scans)
	}
	fp.table[5002] = 44
	a.Lookup([]int{5000, 5001, 5002})
	a.Lookup([]int{5000, 5001, 5002})
	if fp.scans != 2 {
		t.Fatalf("新端口只该多扫一次，共 %d 次", fp.scans)
	}
	// 端口消失后重新出现，算新端口
	a.Lookup([]int{5000})
	a.Lookup([]int{5000, 5002})
	if fp.scans != 3 {
		t.Fatalf("端口重现应重新反查，共 %d 次", fp.scans)
	}
}

func TestAttributorRetriesUnknownPortBounded(t *testing.T) {
	fp := &fakePlat{table: map[int]int{}}
	a := NewWith(fp)
	for i := 0; i < 10; i++ {
		a.Lookup([]int{7000})
	}
	if fp.scans != maxMiss {
		t.Fatalf("查不到的端口最多试 %d 次，实际 %d", maxMiss, fp.scans)
	}
}

func chain(paths ...string) []Ancestor {
	out := make([]Ancestor, len(paths))
	for i, p := range paths {
		out[i] = Ancestor{PID: 100 + i, PPID: 101 + i, Path: p}
	}
	return out
}

func TestCallerApp(t *testing.T) {
	cases := []struct {
		name  string
		chain []Ancestor
		want  string
	}{
		{"Mirasim 会话：claude → mirasim-reclaude ×2 → Mirasim.app", chain(
			"/Users/u/.local/bin/claude",
			"/Users/u/.fleet-dao/mirasim-reclaude/releases/abc/mirasim-reclaude",
			"/Users/u/.fleet-dao/mirasim-reclaude/releases/abc/mirasim-reclaude",
			"/Applications/Mirasim.app/Contents/MacOS/Mirasim",
			"/Applications/Mirasim.app/Contents/MacOS/Mirasim"), "Mirasim"},
		{"reclaude 会话（终端里起）", chain(
			"/Users/u/.local/bin/claude", "/Users/u/.local/bin/reclaude", "-zsh", "/usr/bin/login",
			"/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal"), "reclaude"},
		{"reclaude 会话（launchd 起）", chain("/Users/u/.local/bin/claude", "/Users/u/.local/bin/reclaude"), "reclaude"},
		{"claude 是 node 起的、外面是 reclaude", chain("/usr/local/bin/node", "/bin/sh", "/opt/reclaude"), "reclaude"},
		{"终端里直接跑 claude：找不到", chain(
			"/Users/u/.local/bin/claude", "/bin/zsh", "/usr/bin/login",
			"/Applications/iTerm.app/Contents/MacOS/iTerm2"), ""},
		{"VS Code 里的 claude", chain(
			"/Users/u/.local/bin/claude", "/bin/zsh",
			"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper"), "Visual Studio Code"},
		{"Windows：reclaude.exe", chain(`claude.exe`, `reclaude.exe`, `pwsh.exe`, `WindowsTerminal.exe`, `explorer.exe`), "reclaude"},
		{"Windows：直接在终端跑 claude", chain(`claude.exe`, `powershell.exe`, `explorer.exe`), ""},
		{"空链", nil, ""},
	}
	for _, c := range cases {
		if got := CallerApp(c.chain); got != c.want {
			t.Errorf("%s：得到 %q，要 %q", c.name, got, c.want)
		}
	}
}

func TestAncestryOfSelf(t *testing.T) {
	chain, err := Ancestry(os.Getpid(), MaxCallerAncestry)
	if err != nil {
		t.Fatalf("上溯自己失败：%v", err)
	}
	if len(chain) == 0 || chain[0].PID != os.Getpid() || chain[0].PPID != os.Getppid() || chain[0].Path == "" {
		t.Fatalf("第一层应当是自己：%+v（ppid %d）", chain, os.Getppid())
	}
	if len(chain) > MaxCallerAncestry {
		t.Fatalf("最多 %d 层，拿了 %d 层", MaxCallerAncestry, len(chain))
	}
	if _, err := Ancestry(1, 3); err == nil {
		t.Fatal("pid 1 没有可上溯的父进程，应当报错")
	}
}

// hook 每次工具调用前后各跑一次：go test -bench HookCaller ./internal/procattr 看上溯一次多久。
func BenchmarkHookCaller(b *testing.B) {
	for b.Loop() {
		HookCaller()
	}
}
