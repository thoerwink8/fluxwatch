package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/engine"
	"github.com/thoerwink8/fluxwatch/internal/mihomo"
	"github.com/thoerwink8/fluxwatch/internal/model"
)

// targets.yaml 写坏时引擎不再一直起不来：面板写明「配置第 N 行有错」，定时重试，改好后自动起，订阅者不用重新订阅。
func TestHealingRetriesUntilConfigFixed(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "targets.yaml")
	if err := os.WriteFile(cfg, []byte("targets:\n  - name: a\n    sources: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := func() (runner, error) {
		return engine.New(engine.Options{
			ConfigPath: cfg, LedgerPath: filepath.Join(dir, "ledger.jsonl"), HookPath: filepath.Join(dir, "hook.jsonl"),
			SettingsPath: filepath.Join(dir, "settings.json"), Interval: 20 * time.Millisecond,
			Locate: func(context.Context, mihomo.Manual) (mihomo.Endpoint, error) {
				return mihomo.Endpoint{}, errors.New("测试里没有 mihomo")
			},
		})
	}
	_, err := start()
	if err == nil {
		t.Fatal("坏配置应当起不来")
	}
	h := newHealing(start, 30*time.Millisecond, err)
	ch, unsub := h.Subscribe()
	defer unsub()

	s := h.Snapshot()
	if s.Worst != model.StateUnknown || len(s.SourceErr) != 1 || !strings.HasPrefix(s.SourceErr[0], "配置第 3 行有错：") {
		t.Fatalf("起不来时的快照：%+v", s)
	}
	if _, err := h.Discover(); err == nil || h.AddTarget("x", "x.example.com") == nil {
		t.Fatal("起不来时别的调用也要照实报错")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.run(ctx); close(done) }()

	// 还坏着：重试时照样推「起不来」的快照
	select {
	case s := <-ch:
		if !strings.HasPrefix(s.SourceErr[0], "配置第 3 行有错") {
			t.Fatalf("重试中的快照：%+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("重试时没推快照")
	}

	if err := os.WriteFile(cfg, []byte("targets:\n  - name: a\n    sources: [{ type: mihomo, hosts: [\"*.example.com\"] }]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s := <-ch:
			if len(s.Targets) == 1 && s.Targets[0].Name == "a" {
				if h.Snapshot().Targets == nil {
					t.Fatal("起来后 Snapshot 要转给真引擎")
				}
				cancel()
				<-done
				h.wait()
				return
			}
		case <-deadline:
			t.Fatalf("改好后没自动起来：%+v", h.Snapshot())
		}
	}
}

func TestStartReason(t *testing.T) {
	if got := startReason(errors.New("targets.yaml：yaml: line 7: did not find expected key")); !strings.HasPrefix(got, "配置第 7 行有错：") {
		t.Error(got)
	}
	if got := startReason(errors.New("targets.yaml：目标 a 没有数据源")); !strings.HasPrefix(got, "配置有错：") {
		t.Error(got)
	}
	if got := startReason(errors.New("找不到用户目录")); !strings.HasPrefix(got, "引擎起不来：") {
		t.Error(got)
	}
}
