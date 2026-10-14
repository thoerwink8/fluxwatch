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

// status 默认读托盘写的 state.json 并标出快照时间；托盘没开、快照超过 5 秒时退回自己采样并写明「托盘没在跑」。
func TestStatusReadsTrayStateOrFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	now := time.Date(2026, 10, 14, 10, 0, 10, 0, time.Local)
	sampled := 0
	deps := statusDeps{statePath: path, now: func() time.Time { return now }, sample: func(time.Duration) (model.Snapshot, error) {
		sampled++
		return model.Snapshot{Targets: []model.Target{{Name: "self", State: model.StateUnknown}}}, nil
	}}
	run := func(args ...string) (string, string) {
		var out, errw strings.Builder
		if err := status(&out, &errw, args, deps); err != nil {
			t.Fatal(err)
		}
		return out.String(), errw.String()
	}

	out, _ := run()
	if sampled != 1 || !strings.Contains(out, "托盘没在跑（没有 state.json）") || !strings.Contains(out, "self") {
		t.Fatalf("没有 state.json：%q", out)
	}

	write := func(at time.Time) {
		b := `{"writtenAt":"` + at.Format(time.RFC3339Nano) + `","pid":42,"snapshot":{"at":"` + at.Format(time.RFC3339Nano) +
			`","targets":[{"name":"reclaude","state":"stall","layer":"A","reason":"节点 JP-1 字节停了 30s","node":"JP-1"}],"worst":"stall","alerting":1}}`
		if err := os.WriteFile(path, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(now.Add(-2 * time.Second))
	out, _ = run()
	if sampled != 1 || !strings.Contains(out, "快照时间 10:00:08（托盘此刻的状态，2 秒前）") || !strings.Contains(out, "reclaude     stall") {
		t.Fatalf("托盘快照：%q", out)
	}
	out, errOut := run("--json")
	if sampled != 1 || !strings.Contains(errOut, "快照时间 10:00:08") || !strings.Contains(out, `"state": "stall"`) || strings.Contains(out, "快照时间") {
		t.Fatalf("--json：stdout=%q stderr=%q", out, errOut)
	}

	write(now.Add(-6 * time.Second))
	out, _ = run()
	if sampled != 2 || !strings.Contains(out, "托盘没在跑（快照停在 10:00:04，已 6 秒没更新）") {
		t.Fatalf("旧快照：%q", out)
	}

	write(now.Add(-1 * time.Second))
	out, _ = run("--self")
	if sampled != 3 || !strings.Contains(out, "自己采样") || strings.Contains(out, "reclaude") {
		t.Fatalf("--self：%q", out)
	}
}
