package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HookLine 是 hook.jsonl 的一行：只有事件、工具名、时间、会话 id、发起调用的软件，不记工具入参和输出。
type HookLine struct {
	TS      time.Time `json:"ts"`
	Event   string    `json:"event"` // "pre" | "post"
	Tool    string    `json:"tool"`
	Session string    `json:"session"`
	App     string    `json:"app"` // 发起这次调用的软件（reclaude、Mirasim……），hook 顺父进程链找；找不到为空
}

// MaxBusy 是一次工具调用最多算多久「在跑」：超过就当 post 丢了，不再压住报警。
const MaxBusy = 10 * time.Minute

// Hook 读 hook.jsonl 判断此刻有没有本地工具在跑。
type Hook struct {
	tail      *Tailer
	sessions  map[string]*hookSession
	installed bool
}

type hookSession struct {
	open    int
	lastPre time.Time
	app     string // 最近一次 pre 记的软件
}

// Running 是此刻有工具在跑的会话归属。
type Running struct {
	Any  bool            // 有 app 为空的会话在跑（旧格式、找不到发起软件）：对所有目标生效
	Apps map[string]bool // 有工具在跑的软件（normApp 规范化过）
}

// Active 表示有任何工具在跑。
func (r Running) Active() bool { return r.Any || len(r.Apps) > 0 }

// For 判断 names（目标的 apps，没配 apps 时是目标 name）里有没有软件在跑工具；app 为空的会话对谁都算。
func (r Running) For(names []string) bool {
	if r.Any {
		return true
	}
	for _, n := range names {
		if r.Apps[normApp(n)] {
			return true
		}
	}
	return false
}

// NewHook 读 path；第一次打开往回看 64KB，接上已经在跑的工具。
func NewHook(path string) *Hook {
	return &Hook{tail: NewTailer(path, 64<<10), sessions: map[string]*hookSession{}}
}

// Installed 表示 hook.jsonl 存在过（装了 hook）。没装不判 busy。
func (h *Hook) Installed() bool { return h.installed }

// Busy 返回此刻是否有任何工具在跑（不分软件）。
func (h *Hook) Busy(now time.Time) (bool, error) {
	r, err := h.Running(now)
	return r.Active(), err
}

// Running 返回此刻哪些软件有工具在跑。文件不存在不算错（没装 hook）；其他读错返回错误。
func (h *Hook) Running(now time.Time) (Running, error) {
	r := Running{Apps: map[string]bool{}}
	lines, err := h.tail.Lines()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if !h.installed {
				return r, nil
			}
			return r, fmt.Errorf("hook.jsonl 不见了：%w", err)
		}
		return r, err
	}
	h.installed = true
	for _, ln := range lines {
		var l HookLine
		if json.Unmarshal([]byte(ln), &l) != nil {
			continue
		}
		s := h.sessions[l.Session]
		if s == nil {
			s = &hookSession{}
			h.sessions[l.Session] = s
		}
		switch l.Event {
		case "pre":
			s.open++
			s.lastPre = l.TS
			s.app = l.App
		case "post":
			if s.open > 0 {
				s.open--
			}
		}
	}
	for id, s := range h.sessions {
		if s.open > 0 && now.Sub(s.lastPre) > MaxBusy {
			s.open = 0
		}
		if s.open > 0 {
			if a := normApp(s.app); a == "" {
				r.Any = true
			} else {
				r.Apps[a] = true
			}
		}
		if s.open == 0 && now.Sub(s.lastPre) > MaxBusy {
			delete(h.sessions, id)
		}
	}
	return r, nil
}

// HookPath 是 ~/.fluxwatch/hook.jsonl。
func HookPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fluxwatch", "hook.jsonl"), nil
}

// claudeHookInput 是 Claude Code hook 从 stdin 给的 JSON 里我们要的字段；tool_input 等一概不解析。
type claudeHookInput struct {
	Event     string `json:"hook_event_name"`
	Tool      string `json:"tool_name"`
	SessionID string `json:"session_id"`
}

// AppendHookEvent 从 Claude Code 的 hook stdin 读事件，追加一行到 hook.jsonl。
// event 非空时（"pre" / "post"）覆盖从输入判断的事件。app 是发起调用的软件（调用方顺父进程链找，找不到传空）。
// 只写 ts / event / tool / session / app。
func AppendHookEvent(path string, in io.Reader, now time.Time, event, app string) error {
	b, err := io.ReadAll(io.LimitReader(in, 8<<20))
	if err != nil {
		return err
	}
	var h claudeHookInput
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &h); err != nil {
			return fmt.Errorf("hook 输入不是 JSON：%w", err)
		}
	}
	if event == "" {
		switch h.Event {
		case "PreToolUse":
			event = "pre"
		case "PostToolUse", "PostToolUseFailure":
			event = "post"
		default:
			return fmt.Errorf("不认识的 hook 事件 %q（只处理 PreToolUse / PostToolUse）", h.Event)
		}
	}
	if event != "pre" && event != "post" {
		return fmt.Errorf("事件只能是 pre 或 post，收到 %q", event)
	}
	line, err := json.Marshal(HookLine{TS: now, Event: event, Tool: h.Tool, Session: h.SessionID, App: app})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := rotateHookLog(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// HookMaxSize 是 hook.jsonl 的上限：写之前超过它就改名成 hook.jsonl.1（覆盖上一份），新开一个。
// 读的一方（Hook）靠文件身份认出轮转，从新文件头读；只留一份旧的，最多占 2MB。
const HookMaxSize = 1 << 20

func rotateHookLog(path string) error {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Size() < HookMaxSize {
		return nil
	}
	// 几个 hook 同时写时可能别人刚转完：文件不见了不算错
	if err := os.Rename(path, path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("hook.jsonl 轮转失败：%w", err)
	}
	return nil
}
