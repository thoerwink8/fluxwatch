package app

import (
	"testing"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

func one(st model.State, recovered bool) model.Snapshot {
	return model.Snapshot{Targets: []model.Target{{Name: "m", Label: "Mirasim", State: st, StuckFor: 31, Recovered: recovered}}}
}

func count(alerts []alert, k alertKind) int {
	n := 0
	for _, a := range alerts {
		if a.kind == k {
			n++
		}
	}
	return n
}

// M4：卡住期间状态被别的东西带走（THINKING、BUSY、SUSPECT）再回到 STALL，是同一次卡住：
// 只发一次「卡住」，不发「已恢复」；只有引擎标了 Recovered 才发恢复。
func TestRecoverOnlyOnRecoveredFlag(t *testing.T) {
	tr := newTransitions()
	var all []alert
	all = append(all, tr.next(one(model.StateOK, false))...)
	for _, st := range []model.State{model.StateSuspect, model.StateStall, model.StateThinking, model.StateStall,
		model.StateBusy, model.StateStall, model.StateSuspect, model.StateStall} {
		all = append(all, tr.next(one(st, false))...)
	}
	if count(all, alertStall) != 1 || count(all, alertRecover) != 0 {
		t.Fatalf("同一次卡住来回翻：卡住 %d 次、恢复 %d 次，要 1 / 0", count(all, alertStall), count(all, alertRecover))
	}
	if a := tr.next(one(model.StateOK, true)); count(a, alertRecover) != 1 {
		t.Fatalf("引擎标了恢复要发一次：%v", a)
	}
	if a := tr.next(one(model.StateOK, false)); len(a) != 0 {
		t.Fatalf("恢复只发一次：%v", a)
	}
}

// 卡住转 IDLE（连接没了收尾、没人等了）不是恢复；之后再卡住是新的一次。
func TestStallToIdleIsNotRecovery(t *testing.T) {
	tr := newTransitions()
	tr.next(one(model.StateOK, false))
	if a := tr.next(one(model.StateStall, false)); count(a, alertStall) != 1 {
		t.Fatalf("进卡住：%v", a)
	}
	if a := tr.next(one(model.StateIdle, false)); len(a) != 0 {
		t.Fatalf("卡住转 IDLE 不发恢复：%v", a)
	}
	if a := tr.next(one(model.StateStall, false)); count(a, alertStall) != 1 {
		t.Fatalf("之后再卡住是新的一次：%v", a)
	}
	// 睡眠唤醒：引擎把卡住按未恢复收尾，状态回到 OK 但不带 Recovered
	if a := tr.next(one(model.StateOK, false)); len(a) != 0 {
		t.Fatalf("未恢复收尾回到 OK 不发恢复：%v", a)
	}
	if a := tr.next(one(model.StateStall, false)); count(a, alertStall) != 1 {
		t.Fatalf("收尾之后再卡住是新的一次：%v", a)
	}
}
