package procattr

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// Ancestor 是父进程链上的一层。
type Ancestor struct {
	PID  int
	PPID int
	Path string // 可执行文件路径（mac、Linux）或映像文件名（Windows）；拿不到时是短进程名
}

// MaxCallerAncestry 是 CallerApp 从 hook 的父进程往上最多看几层。
// 本机实测 Mirasim 在 claude 往上第 3 层（claude → mirasim-reclaude → mirasim-reclaude → Mirasim），留余量。
const MaxCallerAncestry = 8

// genericProcs 是链上的「过道」：Claude Code 自己、shell、运行时、会话管理器。它们不是发起调用的软件，跳过。
var genericProcs = map[string]bool{
	"claude": true, "node": true, "bun": true, "deno": true,
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "tcsh": true, "csh": true, "nu": true,
	"login": true, "sudo": true, "su": true, "env": true, "script": true, "sshd": true,
	"tmux": true, "tmux: server": true, "screen": true, "npx": true, "npm": true, "pnpm": true, "yarn": true,
	"cmd": true, "powershell": true, "pwsh": true, "conhost": true, "openconsole": true,
	"launchd": true, "init": true, "systemd": true,
}

// stopProcs 是到了就不再往上找的进程：终端（Windows 上没有 .app，靠名字认）、桌面外壳。
var stopProcs = map[string]bool{
	"windowsterminal": true, "wt": true, "explorer": true,
}

func procName(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	n := strings.ToLower(strings.TrimSpace(path.Base(p)))
	n = strings.TrimPrefix(n, "-") // 登录 shell 形如「-zsh」
	return strings.TrimSuffix(n, ".exe")
}

func displayName(p string) string {
	n := path.Base(strings.ReplaceAll(p, `\`, "/"))
	n = strings.TrimPrefix(n, "-")
	if strings.HasSuffix(strings.ToLower(n), ".exe") {
		n = n[:len(n)-4]
	}
	return n
}

// CallerApp 从 chain（chain[0] 是 hook 的父进程，一般是 claude，往上依次是祖先）找出发起这次调用的软件：
//
//  1. 跳过 claude、shell、node 这类过道，取第一个不是过道的进程 X；
//  2. X 在某个 .app 里：取最外层 .app 的名字（终端类 .app 不算，说明是命令行里直接跑的 claude，返回空）；
//  3. 否则 X 是命令行程序（reclaude、mirasim-reclaude）：跳过和 X 同名的上层副本，
//     紧挨着的上一层是非终端的 .app 时取那个 .app（mirasim-reclaude 是 Mirasim.app 起的 → Mirasim），否则取 X 的文件名。
//
// 找不到（链上只有过道和终端）返回空：hook.jsonl 里 app 留空，引擎当成对所有目标生效。
func CallerApp(chain []Ancestor) string {
	for i, a := range chain {
		if app := outerApp(a.Path); app != "" {
			if terminals[app] {
				return ""
			}
			return app
		}
		n := procName(a.Path)
		if stopProcs[n] {
			return ""
		}
		if n == "" || genericProcs[n] {
			continue
		}
		j := i + 1
		for j < len(chain) && procName(chain[j].Path) == n {
			j++
		}
		if j < len(chain) {
			if app := outerApp(chain[j].Path); app != "" && !terminals[app] {
				return app
			}
		}
		return displayName(a.Path)
	}
	return ""
}

// Ancestry 从 pid 开始往上取最多 max 层（含 pid 自己），到 pid 1 / 0 或者取不到为止。
// 一层都取不到返回错误（平台不支持或进程已经没了）。
func Ancestry(pid, max int) ([]Ancestor, error) {
	lookupProc, err := procLookup()
	if err != nil {
		return nil, err
	}
	var out []Ancestor
	seen := map[int]bool{}
	if pid <= 1 {
		return nil, fmt.Errorf("进程 %d 没有可上溯的父进程", pid)
	}
	for len(out) < max && pid > 1 && !seen[pid] {
		seen[pid] = true
		a, err := lookupProc(pid)
		if err != nil {
			if len(out) == 0 {
				return nil, err
			}
			break
		}
		out = append(out, a)
		pid = a.PPID
	}
	return out, nil
}

// HookCaller 是 `fluxwatch-cli hook` 用的：从自己的父进程往上找发起调用的软件。找不到或出错返回空。
func HookCaller() string {
	chain, err := Ancestry(os.Getppid(), MaxCallerAncestry)
	if err != nil {
		return ""
	}
	return CallerApp(chain)
}
