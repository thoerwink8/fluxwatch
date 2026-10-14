package source

import (
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
	// 轮转：旧文件改名后又写了一行，新文件出现
	appendFile(t, p, "tail of old\n")
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, "new file\n")
	if got := lines(t, tl); !reflect.DeepEqual(got, []string{"tail of old", "new file"}) {
		t.Fatalf("轮转后 = %v", got)
	}
	// 文件没了 → 报错
	os.Remove(p)
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
	if busy, err := h.Busy(now); busy || err != nil || h.Installed() {
		t.Fatalf("没装 hook：busy=%v err=%v", busy, err)
	}
	in := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"SECRET-CONTENT"}}`
	if err := AppendHookEvent(p, strings.NewReader(in), now, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "tool_input") {
		t.Fatalf("hook.jsonl 不该记工具入参：%s", b)
	}
	if busy, err := h.Busy(now.Add(time.Second)); !busy || err != nil {
		t.Fatalf("pre 之后应当 busy：%v %v", busy, err)
	}
	in = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash","tool_response":"x"}`
	if err := AppendHookEvent(p, strings.NewReader(in), now.Add(5*time.Second), ""); err != nil {
		t.Fatal(err)
	}
	if busy, _ := h.Busy(now.Add(6 * time.Second)); busy {
		t.Fatal("post 之后不该 busy")
	}
	// pre 没等到 post：超过 MaxBusy 不再压住
	if err := AppendHookEvent(p, strings.NewReader(`{"session_id":"s2","tool_name":"Bash"}`), now, "pre"); err != nil {
		t.Fatal(err)
	}
	if busy, _ := h.Busy(now.Add(time.Minute)); !busy {
		t.Fatal("s2 在跑")
	}
	if busy, _ := h.Busy(now.Add(MaxBusy + time.Minute)); busy {
		t.Fatal("超过 MaxBusy 应当放开")
	}
	if err := AppendHookEvent(p, strings.NewReader(`{"hook_event_name":"Stop"}`), now, ""); err == nil {
		t.Fatal("不认识的事件应当报错")
	}
}
