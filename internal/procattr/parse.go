package procattr

import (
	"bufio"
	"path"
	"strconv"
	"strings"
)

// parseLsof 解析 `lsof -Fpcn` 的输出，取每条连接本地端口 → PID。
// 形如：p<pid> / c<命令> / f<fd> / n<本地>-><远端>。
func parseLsof(out string) map[int]int {
	m := map[int]int{}
	pid := 0
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			local, _, ok := strings.Cut(line[1:], "->")
			if !ok || pid <= 0 {
				continue
			}
			i := strings.LastIndexByte(local, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(local[i+1:])
			if err != nil {
				continue
			}
			if _, dup := m[port]; !dup {
				m[port] = pid
			}
		}
	}
	return m
}

type psEntry struct {
	ppid int
	comm string
}

// parsePs 解析 `ps -Ao pid=,ppid=,comm=`；comm 是完整路径，可能带空格。
func parsePs(out string) map[int]psEntry {
	m := map[int]psEntry{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		// comm 从第三列开始到行尾（保留内部空格）
		line := strings.TrimSpace(sc.Text())
		rest := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, f[1]))
		m[pid] = psEntry{ppid: ppid, comm: rest}
	}
	return m
}

// outerApp 取路径里最外层的 X.app 名（「/Applications/Mirasim.app/.../Mirasim Helper.app/...」→ Mirasim）。
func outerApp(p string) string {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasSuffix(seg, ".app") && len(seg) > 4 {
			return strings.TrimSuffix(seg, ".app")
		}
	}
	return ""
}

// terminals 是终端类软件：上溯到它们时说明是命令行里跑的，报命令本身更有用。
var terminals = map[string]bool{
	"Terminal": true, "iTerm": true, "iTerm2": true, "Ghostty": true, "WezTerm": true,
	"Alacritty": true, "kitty": true, "Warp": true, "Hyper": true, "Tabby": true,
}

// MaxAncestry 是上溯 ppid 的最多层数。
const MaxAncestry = 6

// appFromTree 沿 ppid 上溯最多 6 层，遇到 .app 取它的名字；都没有就取进程自己的文件名。
// 上溯到终端时取进程自己的文件名（命令行工具比「Terminal」有用）。
func appFromTree(pid int, tree map[int]psEntry) string {
	self, ok := tree[pid]
	if !ok {
		return ""
	}
	cur := pid
	for i := 0; i <= MaxAncestry; i++ {
		e, ok := tree[cur]
		if !ok {
			break
		}
		if app := outerApp(e.comm); app != "" {
			if terminals[app] && i > 0 {
				break
			}
			return app
		}
		if e.ppid <= 1 || e.ppid == cur {
			break
		}
		cur = e.ppid
	}
	return path.Base(self.comm)
}
