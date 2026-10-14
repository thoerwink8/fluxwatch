package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/config"
)

func TestMatchHost(t *testing.T) {
	cases := []struct {
		p, h string
		ok   bool
	}{
		{"*.reclaude.ai", "misaka.route.reclaude.ai", true},
		{"*.reclaude.ai", "reclaude.ai", true},
		{"*.reclaude.ai", "notreclaude.ai", false},
		{"*.reclaude.ai", "reclaude.ai.example.com", false},
		{"api.example.com", "API.example.com", true},
		{"api.*.com", "api.example.com", true},
		{"*.mirasim.ai", "", false},
	}
	for _, c := range cases {
		if got := MatchHost(c.p, c.h); got != c.ok {
			t.Errorf("MatchHost(%q,%q)=%v 要 %v", c.p, c.h, got, c.ok)
		}
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func lines(t *testing.T, tl *Tailer) []string {
	t.Helper()
	l, err := tl.Lines()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestTailerNewLinesTruncateRotate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.log")
	appendFile(t, p, "old line\n")
	tl := NewTailer(p, 0)
	defer tl.Close()
	if got := lines(t, tl); len(got) != 0 {
		t.Fatalf("从末尾开始，不该读到旧行：%v", got)
	}
	appendFile(t, p, "one\ntw")
	if got := lines(t, tl); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("新增 = %v", got)
	}
	appendFile(t, p, "o\n")
	if got := lines(t, tl); !reflect.DeepEqual(got, []string{"two"}) {
		t.Fatalf("半行拼接 = %v", got)
	}
	// 截断
	if err := os.WriteFile(p, []byte("after truncate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lines(t, tl); !reflect.DeepEqual(got, []string{"after truncate"}) {
		t.Fatalf("截断后 = %v", got)
	}
	// 轮转：读完之后被盯的程序要能改名（Windows 上开着的文件改不了名），新文件从头读
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatalf("跟读不该占着文件：%v", err)
	}
	appendFile(t, p, "new file\n")
	if got := lines(t, tl); !reflect.DeepEqual(got, []string{"new file"}) {
		t.Fatalf("轮转后 = %v", got)
	}
	appendFile(t, p, "more\n")
	if got := lines(t, tl); !reflect.DeepEqual(got, []string{"more"}) {
		t.Fatalf("轮转后续读 = %v", got)
	}
	// 文件没了 → 报错
	if err := os.Remove(p); err != nil {
		t.Fatalf("跟读不该占着文件：%v", err)
	}
	if _, err := tl.Lines(); err == nil {
		t.Fatal("文件没了应当报错")
	}
}

func TestLogtailClassifies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.log")
	appendFile(t, p, "")
	lt, err := NewLogtail(config.Source{Type: "logtail", Path: p, Error: ` (429|50[0-9]) `, Progress: `event=turn-(first-output|result)`})
	if err != nil {
		t.Fatal(err)
	}
	defer lt.Close()
	if _, err := lt.Poll(); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, strings.Join([]string{
		"x upstream https://api.example.com 503 bad gateway",
		"x upstream 429 too many",
		"event=turn-first-output generation=1",
		"account quota exceeded, switching",
		"ordinary line",
	}, "\n")+"\n")
	r, err := lt.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Errors, []string{"503", "429"}) || r.Progress != 1 || r.Account != 1 {
		t.Fatalf("归类 = %+v", r)
	}
}

func TestLogtailMissingFileIsError(t *testing.T) {
	lt, err := NewLogtail(config.Source{Type: "logtail", Path: filepath.Join(t.TempDir(), "nope.log")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lt.Poll(); err == nil {
		t.Fatal("读不到的日志必须报错，不能当成没有新行")
	}
}

func TestHookBusyAndAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	if busy, err := hookBusy(h, now); busy || err != nil {
		t.Fatalf("没装 hook：busy=%v err=%v", busy, err)
	}
	in := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"SECRET-CONTENT"}}`
	if err := AppendHookEvent(p, strings.NewReader(in), now, "", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "tool_input") {
		t.Fatalf("hook.jsonl 不该记工具入参：%s", b)
	}
	if busy, err := hookBusy(h, now.Add(time.Second)); !busy || err != nil {
		t.Fatalf("pre 之后应当 busy：%v %v", busy, err)
	}
	in = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash","tool_response":"x"}`
	if err := AppendHookEvent(p, strings.NewReader(in), now.Add(5*time.Second), "", ""); err != nil {
		t.Fatal(err)
	}
	if busy, _ := hookBusy(h, now.Add(6*time.Second)); busy {
		t.Fatal("post 之后不该 busy")
	}
	// pre 没等到 post：超过 MaxBusy 不再压住
	if err := AppendHookEvent(p, strings.NewReader(`{"session_id":"s2","tool_name":"Bash"}`), now, "pre", ""); err != nil {
		t.Fatal(err)
	}
	if busy, _ := hookBusy(h, now.Add(time.Minute)); !busy {
		t.Fatal("s2 在跑")
	}
	if busy, _ := hookBusy(h, now.Add(MaxBusy+time.Minute)); busy {
		t.Fatal("超过 MaxBusy 应当放开")
	}
	if err := AppendHookEvent(p, strings.NewReader(`{"hook_event_name":"Notification"}`), now, "", ""); err == nil {
		t.Fatal("不认识的事件应当报错")
	}
}

func TestMatchApp(t *testing.T) {
	cases := []struct {
		apps []string
		app  string
		ok   bool
	}{
		{nil, "ego lite", true}, // 不限软件
		{[]string{"reclaude"}, "reclaude", true},
		{[]string{"reclaude"}, "ReClaude", true},
		{[]string{"Mirasim"}, "mirasim.exe", true},
		{[]string{"reclaude"}, "ego lite", false},
		{[]string{"reclaude"}, "", false}, // 限了软件、又不知道是谁发的：不算
	}
	for _, c := range cases {
		if got := MatchApp(c.apps, c.app); got != c.ok {
			t.Errorf("MatchApp(%v, %q) = %v，要 %v", c.apps, c.app, got, c.ok)
		}
	}
}

func TestTailerFileCreatedLaterReadFromStart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "late.log")
	tl := NewTailer(p, 0)
	if _, err := tl.Lines(); err == nil {
		t.Fatal("文件不存在要报错")
	}
	appendFile(t, p, "first\n")
	if got := lines(t, tl); !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("盯着时才建出来的文件要从头读：%v", got)
	}
}

func TestHookLogRotatesOver1MB(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	if err := os.WriteFile(p, []byte(strings.Repeat("x", HookMaxSize-10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hookBusy(h, now)
	pre := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash"}`
	// 还没到 1MB：照常追加
	if err := AppendHookEvent(p, strings.NewReader(pre), now, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".1"); err == nil {
		t.Fatal("没到 1MB 不该轮转")
	}
	// 过了 1MB：下一次写之前转成 hook.jsonl.1，新文件只有新的一行
	if err := AppendHookEvent(p, strings.NewReader(`{"session_id":"s2","hook_event_name":"PreToolUse","tool_name":"Bash"}`), now, "", ""); err != nil {
		t.Fatal(err)
	}
	old, err := os.Stat(p + ".1")
	if err != nil || old.Size() < HookMaxSize {
		t.Fatalf("应当轮转出 hook.jsonl.1：%v", err)
	}
	b, _ := os.ReadFile(p)
	if n := strings.Count(string(b), "\n"); n != 1 || !strings.Contains(string(b), `"s2"`) {
		t.Fatalf("新文件应当只有一行：%q", b)
	}
	// 读的一方认出轮转，从新文件头读到 s2 的 pre
	busy, err := hookBusy(h, now.Add(time.Second))
	if err != nil || !busy {
		t.Fatalf("轮转后 busy=%v err=%v", busy, err)
	}
}

func TestCheckHookInstall(t *testing.T) {
	cases := []struct {
		name, json string
		want       HookInstall
	}{
		{"readme mac", `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"$HOME/.fluxwatch/bin/fluxwatch-cli hook"}]}],"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"$HOME/.fluxwatch/bin/fluxwatch-cli hook"}]}]}}`, HookInstall{Pre: true, Post: true}},
		{"readme windows", `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"C:/Users/me/.fluxwatch/bin/fluxwatch-cli.exe hook"}]}],"PostToolUse":[{"hooks":[{"type":"command","command":"\"C:\\Users\\me\\.fluxwatch\\bin\\fluxwatch-cli.exe\" hook post"}]}]}}`, HookInstall{Pre: true, Post: true}},
		{"no hooks", `{"model":"x"}`, HookInstall{}},
		{"only mention", `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo fluxwatch-cli"}]}],"PostToolUse":[{"hooks":[{"type":"command","command":"my-fluxwatch-cli hook"}]}]}}`, HookInstall{}},
		{"matcher Bash", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"fluxwatch-cli hook"}]}]}}`, HookInstall{PreOnly: true}},
		{"guarded mac", `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true"}]}],"PostToolUse":[{"hooks":[{"type":"command","command":"[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true"}]}]}}`, HookInstall{Pre: true, Post: true}},
		{"guard only, no hook call", `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] || true"}]}]}}`, HookInstall{}},
	}
	for _, c := range cases {
		got, err := CheckHookInstall([]byte(c.json))
		got.Missing, got.Binary = nil, ""
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v %v，要 %+v", c.name, got, err, c.want)
		}
	}
	if _, err := CheckHookInstall([]byte(`{"hooks":`)); err == nil {
		t.Error("坏 JSON 要报错")
	}
}

// README 里给的两段可粘贴片段（mac、Windows）必须被设置窗判成「已装」。
func TestReadmeHookSnippetsCountAsInstalled(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(b), "```json\n")
	if len(parts) != 3 {
		t.Fatalf("README 里应当正好有 mac、Windows 两段 json 片段，有 %d 段", len(parts)-1)
	}
	for i, p := range parts[1:] {
		snippet := strings.Replace(p[:strings.Index(p, "```")], "<you>", "me", -1)
		got, err := CheckHookInstall([]byte(snippet))
		if err != nil || !got.Installed() || len(got.Missing) != 0 || !strings.Contains(got.Binary, "/.fluxwatch/bin/fluxwatch-cli") {
			t.Errorf("第 %d 段：%+v %v", i+1, got, err)
		}
		// 片段里每条命令都要先查二进制在不在，不在也退出 0
		if n := strings.Count(snippet, `"command":`); n == 0 || n != strings.Count(snippet, `hook || true"`) || n != strings.Count(snippet, `[ -x `) {
			t.Errorf("第 %d 段：每条命令都要用「二进制不在也不报错」的写法", i+1)
		}
	}
}

func TestHookRunningByApp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	pre := func(session string) string {
		return `{"session_id":"` + session + `","hook_event_name":"PreToolUse","tool_name":"Bash"}`
	}
	if err := AppendHookEvent(p, strings.NewReader(pre("rc")), now, "", "reclaude"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"app":"reclaude"`) {
		t.Fatalf("hook.jsonl 应当记 app：%s", b)
	}
	r, err := h.Running(now.Add(time.Second))
	if err != nil || r.Any || !r.Active() {
		t.Fatalf("reclaude 在跑：%+v %v", r, err)
	}
	if !r.For([]string{"ReClaude"}) || !r.For([]string{"reclaude.exe"}) {
		t.Fatal("软件名不分大小写、不算 .exe")
	}
	if r.For([]string{"Mirasim"}) || r.For([]string{"mirasim"}) {
		t.Fatal("reclaude 在跑工具不该算 Mirasim 忙")
	}
	// 旧格式（没有 app 字段）和找不到发起软件（app 为空）都对所有目标生效
	appendLine(t, p, `{"ts":"`+now.Format(time.RFC3339)+`","event":"pre","tool":"Bash","session":"old"}`)
	r, _ = h.Running(now.Add(2 * time.Second))
	if !r.Any || !r.For([]string{"Mirasim"}) {
		t.Fatalf("没有 app 的记录应当对所有目标生效：%+v", r)
	}
	appendLine(t, p, `{"ts":"`+now.Format(time.RFC3339)+`","event":"post","tool":"Bash","session":"old"}`)
	if err := AppendHookEvent(p, strings.NewReader(pre("blank")), now, "", ""); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Running(now.Add(3 * time.Second))
	if !r.Any {
		t.Fatalf("app 为空也对所有目标生效：%+v", r)
	}
}

// hookBusy 是测试用的：此刻有没有任何工具在跑（不分软件）。
func hookBusy(h *Hook, now time.Time) (bool, error) {
	r, err := h.Running(now)
	return r.Active(), err
}

func appendLine(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestHookCommandBinary(t *testing.T) {
	cases := map[string]string{
		`$HOME/.fluxwatch/bin/fluxwatch-cli hook`:                                                          `$HOME/.fluxwatch/bin/fluxwatch-cli`,
		`[ -x "$HOME/.fluxwatch/bin/fluxwatch-cli" ] && "$HOME/.fluxwatch/bin/fluxwatch-cli" hook || true`: `$HOME/.fluxwatch/bin/fluxwatch-cli`,
		`"C:\Users\me\.fluxwatch\bin\fluxwatch-cli.exe" hook post`:                                         `C:\Users\me\.fluxwatch\bin\fluxwatch-cli.exe`,
		`fluxwatch-cli hook`: `fluxwatch-cli`,
		`echo fluxwatch-cli`: ``,
	}
	for cmd, want := range cases {
		if got := HookCommandBinary(cmd); got != want {
			t.Errorf("HookCommandBinary(%q) = %q，要 %q", cmd, got, want)
		}
	}
	if got := ExpandHookBinary(`$HOME/.fluxwatch/bin/fluxwatch-cli`, "/h"); got != "/h/.fluxwatch/bin/fluxwatch-cli" {
		t.Errorf("展开 $HOME：%q", got)
	}
	if got := ExpandHookBinary(`~/.fluxwatch/bin/fluxwatch-cli`, "/h"); got != "/h/.fluxwatch/bin/fluxwatch-cli" {
		t.Errorf("展开 ~：%q", got)
	}
	if got := ExpandHookBinary(`%USERPROFILE%\.fluxwatch\bin\fluxwatch-cli.exe`, `C:\Users\me`); got != `C:\Users\me\.fluxwatch\bin\fluxwatch-cli.exe` {
		t.Errorf("展开 %%USERPROFILE%%：%q", got)
	}
}

func TestCheckHookInstallExtraEvents(t *testing.T) {
	cmd := `{"type":"command","command":"fluxwatch-cli hook"}`
	full := `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[` + cmd + `]}],"PostToolUse":[{"matcher":"*","hooks":[` + cmd + `]}],` +
		`"PostToolUseFailure":[{"matcher":"*","hooks":[` + cmd + `]}],"UserPromptSubmit":[{"hooks":[` + cmd + `]}],"Stop":[{"hooks":[` + cmd + `]}],"StopFailure":[{"hooks":[` + cmd + `]}],` +
		`"SubagentStop":[{"hooks":[` + cmd + `]}],"SessionEnd":[{"hooks":[` + cmd + `]}],"PermissionDenied":[{"matcher":"*","hooks":[` + cmd + `]}]}}`
	got, err := CheckHookInstall([]byte(full))
	if err != nil || !got.Installed() || len(got.Missing) != 0 || got.Binary != "fluxwatch-cli" {
		t.Fatalf("全装：%+v %v", got, err)
	}
	got, _ = CheckHookInstall([]byte(`{"hooks":{"PreToolUse":[{"hooks":[` + cmd + `]}],"PostToolUse":[{"hooks":[` + cmd + `]}],"Stop":[{"hooks":[` + cmd + `]}]}}`))
	if !got.Installed() || !reflect.DeepEqual(got.Missing, []string{"PostToolUseFailure", "UserPromptSubmit", "StopFailure", "SubagentStop", "SessionEnd", "PermissionDenied"}) {
		t.Fatalf("只装了工具和 Stop：%+v", got)
	}
}

// hookEv 拼一条 Claude Code 的 hook 输入。
func hookEv(event, session, agent, toolUse string) string {
	m := map[string]string{"hook_event_name": event, "session_id": session, "tool_name": "Bash"}
	if agent != "" {
		m["agent_id"] = agent
	}
	if toolUse != "" {
		m["tool_use_id"] = toolUse
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func mustAppend(t *testing.T, p, in string, at time.Time, app string) {
	t.Helper()
	if err := AppendHookEvent(p, strings.NewReader(in), at, "", app); err != nil {
		t.Fatal(err)
	}
}

// M1：漏一条 post 只让那一次调用最多算 10 分钟；之后正常的 pre/post 不能把它续上。
func TestHookLostPostExpiresPerCall(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	mustAppend(t, p, hookEv("PreToolUse", "s1", "", "lost"), t0, "Mirasim")
	for i := 1; i <= 15; i++ {
		at := t0.Add(time.Duration(i) * time.Minute)
		id := fmt.Sprintf("ok%d", i)
		mustAppend(t, p, hookEv("PreToolUse", "s1", "", id), at, "Mirasim")
		mustAppend(t, p, hookEv("PostToolUse", "s1", "", id), at.Add(2*time.Second), "Mirasim")
		r, err := h.Running(at.Add(10 * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		busy := r.For([]string{"Mirasim"})
		if want := at.Add(10*time.Second).Sub(t0) <= MaxBusy; busy != want {
			t.Fatalf("第 %d 分钟：busy=%v，要 %v", i, busy, want)
		}
	}
}

// 没有 tool_use_id 的旧记录：按会话先进先出逐条配，每条单独计时。
func TestHookFIFOWithoutToolUseID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	mustAppend(t, p, hookEv("PreToolUse", "s1", "", ""), t0, "reclaude")
	mustAppend(t, p, hookEv("PreToolUse", "s1", "", ""), t0.Add(5*time.Minute), "reclaude")
	mustAppend(t, p, hookEv("PostToolUse", "s1", "", ""), t0.Add(6*time.Minute), "reclaude")
	// 先进先出：结束的是 t0 那条，t0+5m 那条还在跑，算到 t0+15m
	if busy, _ := hookBusy(h, t0.Add(12*time.Minute)); !busy {
		t.Fatal("第二条还在跑")
	}
	if busy, _ := hookBusy(h, t0.Add(15*time.Minute+time.Second)); busy {
		t.Fatal("第二条超过 10 分钟应当放开")
	}
}

// 收到 Stop / StopFailure / SessionEnd 立刻清掉这个会话没结束的调用；SubagentStop 只清那个子代理的。
func TestHookStopClearsSession(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	for _, ev := range []string{"Stop", "StopFailure", "SessionEnd"} {
		p := filepath.Join(t.TempDir(), "hook.jsonl")
		h := NewHook(p)
		mustAppend(t, p, hookEv("PreToolUse", "s1", "", "a"), t0, "Mirasim")
		mustAppend(t, p, hookEv("PreToolUse", "s1", "sub1", "b"), t0, "Mirasim")
		mustAppend(t, p, hookEv("PreToolUse", "s2", "", "c"), t0, "reclaude")
		mustAppend(t, p, hookEv(ev, "s1", "", ""), t0.Add(time.Second), "Mirasim")
		r, _ := h.Running(t0.Add(2 * time.Second))
		if r.For([]string{"Mirasim"}) || !r.For([]string{"reclaude"}) {
			t.Fatalf("%s 之后：%+v", ev, r)
		}
	}
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	mustAppend(t, p, hookEv("PreToolUse", "s1", "sub1", "b"), t0, "Mirasim")
	mustAppend(t, p, hookEv("PreToolUse", "s1", "sub2", "c"), t0, "Mirasim")
	mustAppend(t, p, hookEv("SubagentStop", "s1", "sub1", ""), t0.Add(time.Second), "Mirasim")
	if busy, _ := hookBusy(h, t0.Add(2*time.Second)); !busy {
		t.Fatal("sub2 还在跑")
	}
	mustAppend(t, p, hookEv("SubagentStop", "s1", "sub2", ""), t0.Add(3*time.Second), "Mirasim")
	if busy, _ := hookBusy(h, t0.Add(4*time.Second)); busy {
		t.Fatal("两个子代理都结束了")
	}
	// PermissionDenied：pre 之后被拒，不会再有 post
	mustAppend(t, p, hookEv("PreToolUse", "s1", "", "d"), t0.Add(5*time.Second), "Mirasim")
	mustAppend(t, p, hookEv("PermissionDenied", "s1", "", "d"), t0.Add(6*time.Second), "Mirasim")
	if busy, _ := hookBusy(h, t0.Add(7*time.Second)); busy {
		t.Fatal("被拒的调用应当结束")
	}
}

// 给 #21 的在飞起点：UserPromptSubmit 或最后一个 post 之后、下一个 pre 或 Stop 之前。
func TestHookFlight(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	mira := []string{"Mirasim"}
	check := func(at time.Time, wantSince time.Time, msg string) {
		t.Helper()
		r, err := h.Running(at)
		if err != nil {
			t.Fatal(err)
		}
		since, ok := r.FlightSince(mira)
		if ok != !wantSince.IsZero() || !since.Equal(wantSince) {
			t.Fatalf("%s：在飞起点 %v（%v），要 %v", msg, since, ok, wantSince)
		}
	}
	if r, _ := h.Running(t0); r.Rounds {
		t.Fatal("还没读到回合事件")
	}
	mustAppend(t, p, hookEv("UserPromptSubmit", "s1", "", ""), t0, "Mirasim")
	check(t0.Add(time.Second), t0, "回合开始")
	if r, _ := h.Running(t0.Add(time.Second)); !r.Rounds {
		t.Fatal("读到 UserPromptSubmit 后 Rounds 为真")
	}
	mustAppend(t, p, hookEv("PreToolUse", "s1", "", "a"), t0.Add(5*time.Second), "Mirasim")
	check(t0.Add(6*time.Second), time.Time{}, "工具在跑不算在飞")
	mustAppend(t, p, hookEv("PostToolUse", "s1", "", "a"), t0.Add(9*time.Second), "Mirasim")
	check(t0.Add(10*time.Second), t0.Add(9*time.Second), "工具结束后又在等回包")
	// 子代理单算：主代理在等，子代理的工具不影响主代理的起点
	mustAppend(t, p, hookEv("PreToolUse", "s1", "sub1", "b"), t0.Add(11*time.Second), "Mirasim")
	check(t0.Add(12*time.Second), t0.Add(9*time.Second), "子代理工具在跑，主代理仍在飞")
	mustAppend(t, p, hookEv("PostToolUse", "s1", "sub1", "b"), t0.Add(13*time.Second), "Mirasim")
	check(t0.Add(14*time.Second), t0.Add(9*time.Second), "取最早的起点")
	mustAppend(t, p, hookEv("Stop", "s1", "", ""), t0.Add(20*time.Second), "Mirasim")
	check(t0.Add(21*time.Second), time.Time{}, "Stop 之后不在飞")
	// app 为空的会话不进 Flight（不知道该算谁）
	mustAppend(t, p, hookEv("UserPromptSubmit", "s2", "", ""), t0.Add(30*time.Second), "")
	if r, _ := h.Running(t0.Add(31 * time.Second)); len(r.Flight) != 0 {
		t.Fatalf("app 为空不进 Flight：%+v", r.Flight)
	}
	// 回合开始后很久没有任何事件：当成 Stop 丢了
	mustAppend(t, p, hookEv("UserPromptSubmit", "s3", "", ""), t0.Add(40*time.Second), "Mirasim")
	check(t0.Add(41*time.Second), t0.Add(40*time.Second), "s3 在飞")
	check(t0.Add(40*time.Second+MaxFlight+time.Second), time.Time{}, "超过 MaxFlight 放开")
}

func TestAppendHookEventRecordsIDs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	in := `{"session_id":"s1","agent_id":"ag1","tool_use_id":"toolu_1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"SECRET"}}`
	mustAppend(t, p, in, now, "Mirasim")
	mustAppend(t, p, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"SECRET-PROMPT"}`, now, "Mirasim")
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "SECRET") {
		t.Fatalf("不该记入参和提示词：%s", b)
	}
	ls := strings.Split(strings.TrimSpace(string(b)), "\n")
	var l HookLine
	if err := json.Unmarshal([]byte(ls[0]), &l); err != nil {
		t.Fatal(err)
	}
	if l.Event != EvPre || l.Session != "s1" || l.Agent != "ag1" || l.ToolUse != "toolu_1" || l.App != "Mirasim" || l.Tool != "Bash" {
		t.Fatalf("pre 行：%+v", l)
	}
	var lp HookLine
	if err := json.Unmarshal([]byte(ls[1]), &lp); err != nil || lp.Event != EvPrompt || lp.ToolUse != "" {
		t.Fatalf("prompt 行：%+v %v", lp, err)
	}
	for ev, want := range map[string]string{"PostToolUseFailure": EvPost, "Stop": EvStop, "StopFailure": EvStopFailure,
		"SubagentStop": EvSubagentStop, "SessionEnd": EvSessionEnd, "PermissionDenied": EvDenied} {
		q := filepath.Join(t.TempDir(), "hook.jsonl")
		mustAppend(t, q, hookEv(ev, "s1", "", ""), now, "")
		b, _ := os.ReadFile(q)
		if !strings.Contains(string(b), `"event":"`+want+`"`) {
			t.Errorf("%s 应当记成 %s：%s", ev, want, b)
		}
	}
	if err := AppendHookEvent(p, strings.NewReader(`{}`), now, "bogus", ""); err == nil {
		t.Fatal("不认识的事件参数要报错")
	}
}

func TestAppendHookErrRotates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.err")
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	if err := AppendHookErr(p, now, "boom\nsecond line"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "2026-10-14T10:00:00Z boom second line\n" {
		t.Fatalf("hook.err：%q", b)
	}
	big := strings.Repeat("x", 4000)
	for i := 0; i < 100; i++ { // 每条截到 1KB，100 条过 64KB
		if err := AppendHookErr(p, now, big); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > HookErrMaxSize+2048 {
		t.Fatalf("hook.err 应当不超过 64KB：%d", fi.Size())
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("应当轮转出 hook.err.1：%v", err)
	}
}
