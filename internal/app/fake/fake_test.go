package fake

import (
	"testing"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

func TestDemoCoversRequiredStates(t *testing.T) {
	p := New()
	s := p.Snapshot()
	got := map[string]model.Target{}
	for _, tg := range s.Targets {
		got[tg.Name] = tg
	}
	if r := got["reclaude"]; r.State != model.StateStall || r.Layer != model.LayerLine || r.StuckFor != 42 {
		t.Errorf("ReClaude 应卡住 42s、A 线路停：%+v", r.State)
	}
	if m := got["mirasim"]; m.State != model.StateSuspect {
		t.Errorf("Mirasim 应是慢（可疑）：%s", m.State)
	}
	if b := got["claude-code"]; b.State != model.StateBusy {
		t.Errorf("应有一个本地忙：%s", b.State)
	}
	if g := got["gateway"]; g.State != model.StateStall || g.Layer != model.LayerUpstream {
		t.Errorf("应有一个上游卡（B）：%s %s", g.State, g.Layer)
	}
	if s.Worst != model.StateStall || s.Alerting != 3 {
		t.Errorf("worst=%s alerting=%d", s.Worst, s.Alerting)
	}
	if n := len(got["reclaude"].Trace); n != traceLen {
		t.Errorf("轨迹应 15 分钟每秒一点：%d", n)
	}
}

func TestStepMovesTrace(t *testing.T) {
	p := New()
	before := p.Snapshot().Targets[0].Trace
	p.mu.Lock()
	for i := 0; i < 5; i++ {
		p.step(before[len(before)-1].At.Add(1e9 * 1))
	}
	p.last = p.build(before[len(before)-1].At)
	p.mu.Unlock()
	after := p.Snapshot().Targets[0]
	if after.StuckFor <= 42 {
		t.Errorf("卡住时长应往上走：%v", after.StuckFor)
	}
}

func TestAddTargetMarksWatched(t *testing.T) {
	p := New()
	if err := p.AddTarget("Example", "*.example.com"); err != nil {
		t.Fatal(err)
	}
	ds, _ := p.Discover()
	for _, d := range ds {
		if d.Host == "cdn.example.com" && !d.Watched {
			t.Error("加入后应标已盯")
		}
	}
	if err := p.AddTarget("Example", "*.example.com"); err == nil {
		t.Error("重名应拒绝")
	}
	if Suggest("misaka.route.reclaude.ai") != "*.reclaude.ai" {
		t.Error("建议通配符不对")
	}
}
