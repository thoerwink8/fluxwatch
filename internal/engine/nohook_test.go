package engine

import (
	"os"
	"testing"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// M7：没装 hook，mirasim 回合里跑一个 45 秒的本地工具（线路静默）：
// 分不清是在等工具还是卡住，回合层最多报到 SUSPECT（慢），不进 STALL、不发通知；快照标 HookMissing 给面板提示。
func TestNoHookTurnLayerOnlySuspect(t *testing.T) {
	h := newHarnessMirasimLog(t, `progress: 'event=turn-(first-output|result)', start: 'event=(launch|turn-first-output)( |$)', end: 'event=(turn-result|reject|once-exit)( |$)'`)
	if err := os.Remove(h.hook); err != nil {
		t.Fatal(err)
	}
	h.mh.add("c2", "relay.mirasim.ai", "JP-1", 50002)
	h.tick()
	if !h.last.HookMissing {
		t.Fatal("没装 hook：快照要标 HookMissing")
	}
	appendTo(t, h.launch, "event=turn-first-output\n")
	h.mh.bump("c2", 0, 8000)
	h.tick()
	seen := map[model.State]int{}
	for i := 0; i < 45; i++ {
		h.tick()
		seen[h.target("mirasim").State]++
	}
	if seen[model.StateStall] != 0 || seen[model.StateSuspect] == 0 {
		t.Fatalf("没装 hook、回合里本地工具跑 45 秒：%v，要只到 SUSPECT、没有 STALL", seen)
	}
	if !h.last.HookMissing {
		t.Fatal("没装 hook：快照要一直标 HookMissing")
	}
	// 字节层不受影响：请求发出一个字节都没回，照样报卡住
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.ticks(30)
	h.want("reclaude", model.StateStall, model.LayerLine)
}

// 装了 hook（hook.jsonl 在）：回合卡住照常进 STALL，不标 HookMissing。
func TestHookPresentTurnLayerStalls(t *testing.T) {
	h := newHarnessMirasimLog(t, `progress: 'event=turn-(first-output|result)', start: 'event=(launch|turn-first-output)( |$)', end: 'event=(turn-result|reject|once-exit)( |$)'`)
	h.mh.add("c2", "relay.mirasim.ai", "JP-1", 50002)
	h.tick()
	appendTo(t, h.launch, "event=turn-first-output\n")
	h.mh.bump("c2", 0, 8000)
	h.tick()
	h.ticks(31)
	h.want("mirasim", model.StateStall, model.LayerLine)
	if h.last.HookMissing {
		t.Fatal("hook.jsonl 在，不该标 HookMissing")
	}
}
