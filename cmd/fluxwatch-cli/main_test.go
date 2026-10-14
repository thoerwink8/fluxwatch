package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

func TestChangesPrintsOnlyTransitions(t *testing.T) {
	prev, prevErr := map[string]model.State{}, map[string]bool{}
	at := time.Date(2026, 10, 14, 10, 0, 1, 0, time.Local)
	s := model.Snapshot{At: at, Targets: []model.Target{{Name: "reclaude", State: model.StateIdle}}}
	if got := changes(s, prev, prevErr); len(got) != 1 || !strings.Contains(got[0], "—→idle") {
		t.Fatalf("首行：%v", got)
	}
	if got := changes(s, prev, prevErr); len(got) != 0 {
		t.Fatalf("没变化不该打印：%v", got)
	}
	s.Targets[0] = model.Target{Name: "reclaude", State: model.StateStall, Layer: model.LayerLine, Reason: "节点 JP-1 字节停了 30s", Node: "JP-1"}
	s.SourceErr = []string{"hook：x"}
	got := changes(s, prev, prevErr)
	if len(got) != 2 || got[0] != "10:00:01  reclaude     idle→stall  A  节点 JP-1 字节停了 30s  JP-1" || !strings.Contains(got[1], "数据源问题") {
		t.Fatalf("变化行：%q", got)
	}
	s.SourceErr = nil
	s.Targets = nil
	got = changes(s, prev, prevErr)
	if len(got) != 2 {
		t.Fatalf("目标删除、问题恢复：%q", got)
	}
}

// M8：hook 子命令出任何错都不往外抛（main 一律退出 0），错误写进 ~/.fluxwatch/hook.err。
func TestHookNeverFailsAndLogsErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	app := func() string { return "Mirasim" }

	hook(nil, strings.NewReader(`{"session_id":"s1","tool_use_id":"t1","hook_event_name":"PreToolUse","tool_name":"Bash"}`), now, app)
	b, err := os.ReadFile(filepath.Join(home, ".fluxwatch", "hook.jsonl"))
	if err != nil || !strings.Contains(string(b), `"tool_use_id":"t1"`) || !strings.Contains(string(b), `"app":"Mirasim"`) {
		t.Fatalf("正常写一行：%s %v", b, err)
	}

	hook(nil, strings.NewReader(`not json`), now, app)
	hook(nil, strings.NewReader(`{}`), now, func() string { panic("boom") })
	e, err := os.ReadFile(filepath.Join(home, ".fluxwatch", "hook.err"))
	if err != nil || !strings.Contains(string(e), "不是 JSON") || !strings.Contains(string(e), "panic：boom") {
		t.Fatalf("错误要写进 hook.err：%q %v", e, err)
	}

	// 目录写不了：hook.jsonl、hook.err 都写不进去，也只是悄悄算了
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	t.Setenv("HOME", ro)
	t.Setenv("USERPROFILE", ro)
	hook(nil, strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop"}`), now, app)
}
