package source

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/config"
)

// #27：hook 按目标的 claude 字段归属。all 收所有会话（发起软件为空的也算）；列表按发起软件；nil 不接。
func TestRunningForClaude(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	all := &config.Claude{All: true}
	mira := &config.Claude{Apps: []string{"Mirasim"}}
	rc := &config.Claude{Apps: []string{"reclaude"}}

	mustAppend(t, p, hookEv("UserPromptSubmit", "m", "", ""), t0, "Mirasim")
	mustAppend(t, p, hookEv("UserPromptSubmit", "term", "", ""), t0.Add(2*time.Second), "")
	r, err := h.Running(t0.Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := r.FlightFor(all); !ok || !s.Equal(t0) {
		t.Fatalf("all 收 Mirasim 起的会话：%v %v", s, ok)
	}
	if s, ok := r.FlightFor(mira); !ok || !s.Equal(t0) {
		t.Fatalf("[Mirasim]：%v %v", s, ok)
	}
	if _, ok := r.FlightFor(rc); ok {
		t.Fatal("[reclaude] 不收 Mirasim 起的会话，也不收发起软件为空的")
	}
	if _, ok := r.FlightFor(nil); ok {
		t.Fatal("没写 claude 不接 hook")
	}
	mustAppend(t, p, hookEv("Stop", "m", "", ""), t0.Add(4*time.Second), "Mirasim")
	r, _ = h.Running(t0.Add(5 * time.Second))
	if s, ok := r.FlightFor(all); !ok || !s.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("all 也收发起软件为空的会话（终端里直接跑的 claude）：%v %v", s, ok)
	}

	mustAppend(t, p, hookEv("PreToolUse", "m2", "", "x"), t0.Add(6*time.Second), "Mirasim")
	r, _ = h.Running(t0.Add(7 * time.Second))
	if !r.BusyFor(all) || !r.BusyFor(mira) || r.BusyFor(rc) || r.BusyFor(nil) {
		t.Fatalf("本地忙：all=%v mira=%v rc=%v nil=%v", r.BusyFor(all), r.BusyFor(mira), r.BusyFor(rc), r.BusyFor(nil))
	}
}
