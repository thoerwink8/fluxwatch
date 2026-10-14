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

// hook.jsonl 里的事件名（Event 字段）。
const (
	EvPre          = "pre"           // PreToolUse：一次工具调用开始
	EvPost         = "post"          // PostToolUse / PostToolUseFailure：这次调用结束
	EvDenied       = "denied"        // PermissionDenied：调用被拒，不会再有 post，当结束算
	EvPrompt       = "prompt"        // UserPromptSubmit：主代理的回合开始
	EvStop         = "stop"          // Stop：主代理的回合结束
	EvStopFailure  = "stop_failure"  // StopFailure：回合因 API 出错结束
	EvSubagentStop = "subagent_stop" // SubagentStop：一个子代理结束（agent_id）
	EvSessionEnd   = "session_end"   // SessionEnd：会话结束
)

// claudeEvents 是 Claude Code 的 hook_event_name → hook.jsonl 的事件名。
var claudeEvents = map[string]string{
	"PreToolUse":         EvPre,
	"PostToolUse":        EvPost,
	"PostToolUseFailure": EvPost,
	"PermissionDenied":   EvDenied,
	"UserPromptSubmit":   EvPrompt,
	"Stop":               EvStop,
	"StopFailure":        EvStopFailure,
	"SubagentStop":       EvSubagentStop,
	"SessionEnd":         EvSessionEnd,
}

// HookLine 是 hook.jsonl 的一行：只有事件、工具名、时间、会话 / 子代理 / 工具调用的标识、发起调用的软件，
// 不记工具入参、输出、提示词。
type HookLine struct {
	TS      time.Time `json:"ts"`
	Event   string    `json:"event"` // 见 Ev* 常量
	Tool    string    `json:"tool"`
	Session string    `json:"session"`
	Agent   string    `json:"agent_id,omitempty"`    // 子代理的 agent_id；主代理为空
	ToolUse string    `json:"tool_use_id,omitempty"` // 工具调用的 tool_use_id：pre 和 post 按它配对；旧记录没有，按会话先进先出配
	App     string    `json:"app"`                   // 发起这次调用的软件（reclaude、Mirasim……），hook 顺父进程链找；找不到为空
}

// MaxBusy 是一次工具调用最多算多久「在跑」：超过就当 post 丢了，不再压住报警。每次调用单独计。
const MaxBusy = 10 * time.Minute

// MaxFlight 是「在飞」最多算多久：回合开始或上一个工具结束之后这么久没有任何事件，当成 Stop 丢了。
const MaxFlight = 30 * time.Minute

// Hook 读 hook.jsonl 判断此刻有没有本地工具在跑、哪些会话在等模型回包。
type Hook struct {
	tail     *Tailer
	sessions map[string]*hookSession
	seen     bool // hook.jsonl 读到过：之后不见了要报错
	rounds   bool // 读到过回合事件（prompt / stop……）
}

type hookCall struct {
	id    string // tool_use_id；旧记录为空
	agent string
	start time.Time
}

type hookSession struct {
	app    string               // 最近一条记录的软件
	calls  []hookCall           // 还没结束的工具调用，按 pre 的先后
	flight map[string]time.Time // agent_id（主代理为 ""）→ 在飞起点：回合开始或最后一个 post 之后、下一个 pre 或 stop 之前
	last   time.Time            // 最近一条记录的时间
}

// Running 是此刻 hook 看到的情况，按发起调用的软件归。
type Running struct {
	Any  bool            // 有 app 为空的会话在跑工具（旧格式、找不到发起软件）：对所有目标生效
	Apps map[string]bool // 有工具在跑的软件（normApp 规范化过）

	// Flight 是软件（normApp 规范化过）→ 最早的在飞起点：这个软件的某个会话（子代理单算）回合开始、或最后一个工具结束之后，
	// 还没到下一个 pre 或 stop，也就是在等模型回包。app 为空的会话不进这里（不知道该算谁，宁可不判）。
	Flight map[string]time.Time
	// Rounds 表示读到过回合事件（UserPromptSubmit / Stop 等），即装了新事件的 hook，Flight 可信；
	// 为 false 时引擎应退回不靠 hook 的判法。
	Rounds bool
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

// FlightSince 返回 names（同 For）里的软件最早的在飞起点；没有在飞的会话返回零值和 false。
// 给引擎判「在等回包」用（#21）：起点之后目标线路一直没有真下行，就是卡住。
func (r Running) FlightSince(names []string) (time.Time, bool) {
	var since time.Time
	for _, n := range names {
		if t, ok := r.Flight[normApp(n)]; ok && (since.IsZero() || t.Before(since)) {
			since = t
		}
	}
	return since, !since.IsZero()
}

// NewHook 读 path；第一次打开往回看 64KB，接上已经在跑的工具。
func NewHook(path string) *Hook {
	return &Hook{tail: NewTailer(path, 64<<10), sessions: map[string]*hookSession{}}
}

// Running 返回此刻哪些软件有工具在跑、哪些在等回包。文件不存在不算错（没装 hook）；其他读错返回错误。
func (h *Hook) Running(now time.Time) (Running, error) {
	r := Running{Apps: map[string]bool{}, Flight: map[string]time.Time{}}
	lines, err := h.tail.Lines()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if !h.seen {
				return r, nil
			}
			return r, fmt.Errorf("hook.jsonl 不见了：%w", err)
		}
		return r, err
	}
	h.seen = true
	for _, ln := range lines {
		var l HookLine
		if json.Unmarshal([]byte(ln), &l) != nil {
			continue
		}
		h.apply(l)
	}
	r.Rounds = h.rounds
	for id, s := range h.sessions {
		live := s.calls[:0]
		for _, c := range s.calls {
			if now.Sub(c.start) <= MaxBusy {
				live = append(live, c)
			}
		}
		s.calls = live
		a := normApp(s.app)
		if len(s.calls) > 0 {
			if a == "" {
				r.Any = true
			} else {
				r.Apps[a] = true
			}
		}
		for ag, t := range s.flight {
			if now.Sub(t) > MaxFlight {
				delete(s.flight, ag)
				continue
			}
			if a != "" {
				if cur, ok := r.Flight[a]; !ok || t.Before(cur) {
					r.Flight[a] = t
				}
			}
		}
		if len(s.calls) == 0 && len(s.flight) == 0 && now.Sub(s.last) > MaxBusy {
			delete(h.sessions, id)
		}
	}
	return r, nil
}

// apply 把一行记进会话状态。
func (h *Hook) apply(l HookLine) {
	s := h.sessions[l.Session]
	if s == nil {
		s = &hookSession{flight: map[string]time.Time{}}
		h.sessions[l.Session] = s
	}
	if l.TS.After(s.last) {
		s.last = l.TS
	}
	if l.App != "" || l.Event == EvPre {
		s.app = l.App
	}
	switch l.Event {
	case EvPre:
		s.calls = append(s.calls, hookCall{id: l.ToolUse, agent: l.Agent, start: l.TS})
		delete(s.flight, l.Agent)
	case EvPost, EvDenied:
		s.endCall(l)
		if !s.hasCalls(l.Agent) {
			s.flight[l.Agent] = l.TS
		}
	case EvPrompt:
		h.rounds = true
		s.flight[l.Agent] = l.TS
	case EvStop, EvStopFailure, EvSessionEnd:
		h.rounds = true
		s.calls = nil
		clear(s.flight)
	case EvSubagentStop:
		h.rounds = true
		kept := s.calls[:0]
		for _, c := range s.calls {
			if c.agent != l.Agent {
				kept = append(kept, c)
			}
		}
		s.calls = kept
		delete(s.flight, l.Agent)
	}
}

// endCall 结束一次调用：有 tool_use_id 按它找（找不到说明 pre 没读到，不动别的调用）；
// 没有就按先进先出结束这个会话（同一个代理优先）最早的一条没有 id 的调用。
func (s *hookSession) endCall(l HookLine) {
	idx := -1
	if l.ToolUse != "" {
		for i, c := range s.calls {
			if c.id == l.ToolUse {
				idx = i
				break
			}
		}
	} else {
		for i, c := range s.calls {
			if c.id == "" && c.agent == l.Agent {
				idx = i
				break
			}
		}
		if idx < 0 {
			for i, c := range s.calls {
				if c.id == "" {
					idx = i
					break
				}
			}
		}
	}
	if idx >= 0 {
		s.calls = append(s.calls[:idx], s.calls[idx+1:]...)
	}
}

func (s *hookSession) hasCalls(agent string) bool {
	for _, c := range s.calls {
		if c.agent == agent {
			return true
		}
	}
	return false
}

// HookPath 是 ~/.fluxwatch/hook.jsonl。
func HookPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fluxwatch", "hook.jsonl"), nil
}

// claudeHookInput 是 Claude Code hook 从 stdin 给的 JSON 里我们要的字段；tool_input、prompt 等一概不解析。
type claudeHookInput struct {
	Event     string `json:"hook_event_name"`
	Tool      string `json:"tool_name"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	ToolUseID string `json:"tool_use_id"`
}

// AppendHookEvent 从 Claude Code 的 hook stdin 读事件，追加一行到 hook.jsonl。
// event 非空时（Ev* 之一）覆盖从输入判断的事件。app 是发起调用的软件（调用方顺父进程链找，找不到传空）。
// 只写 ts / event / tool / session / agent_id / tool_use_id / app。
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
		ev, ok := claudeEvents[h.Event]
		if !ok {
			return fmt.Errorf("不认识的 hook 事件 %q", h.Event)
		}
		event = ev
	}
	if !knownEvent(event) {
		return fmt.Errorf("不认识的事件参数 %q", event)
	}
	line, err := json.Marshal(HookLine{TS: now, Event: event, Tool: h.Tool, Session: h.SessionID, Agent: h.AgentID, ToolUse: h.ToolUseID, App: app})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := rotateLog(path, HookMaxSize); err != nil {
		return fmt.Errorf("hook.jsonl 轮转失败：%w", err)
	}
	return appendBytes(path, append(line, '\n'))
}

func knownEvent(ev string) bool {
	for _, e := range claudeEvents {
		if e == ev {
			return true
		}
	}
	return false
}

// HookMaxSize 是 hook.jsonl 的上限：写之前超过它就改名成 hook.jsonl.1（覆盖上一份），新开一个。
// 读的一方（Hook）靠文件身份认出轮转，从新文件头读；只留一份旧的，最多占 2MB。
const HookMaxSize = 1 << 20

// HookErrMaxSize 是 hook.err 的上限，超过照样轮转成 hook.err.1。
const HookErrMaxSize = 64 << 10

// HookErrPath 是 ~/.fluxwatch/hook.err：hook 子命令出的错写这里，不打扰 Claude Code。
func HookErrPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fluxwatch", "hook.err"), nil
}

// AppendHookErr 往 hook.err 追加一行「时间 错误」，超过 HookErrMaxSize 先轮转。消息截到 1KB。
func AppendHookErr(path string, now time.Time, msg string) error {
	msg = strings.ReplaceAll(strings.TrimSpace(msg), "\n", " ")
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := rotateLog(path, HookErrMaxSize); err != nil {
		return err
	}
	return appendBytes(path, []byte(now.Format(time.RFC3339)+" "+msg+"\n"))
}

func appendBytes(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// rotateLog 在 path 到了 max 字节时改名成 path.1（覆盖上一份）。
func rotateLog(path string, max int64) error {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Size() < max {
		return nil
	}
	// 几个 hook 同时写时可能别人刚转完：文件不见了不算错
	if err := os.Rename(path, path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
