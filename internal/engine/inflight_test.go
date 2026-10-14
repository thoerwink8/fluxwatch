package engine

import (
	"testing"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// #21 M2：靠 hook 判「在飞」。回合开始（UserPromptSubmit）或最后一个工具结束之后、下一个 Pre 或 Stop 之前，
// 目标在等模型回包：这段时间目标连接没有真下行就是卡住。

// 探针 1：发请求、流了 5 秒、之后 180 秒一个字节都没有（回包到一半断流）。
func TestHookFlightMidStreamStall(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.hookEventApp("prompt", "s1", "reclaude")
	h.mh.bump("c1", 20000, 0)
	h.tick()
	for i := 0; i < 5; i++ {
		h.mh.bump("c1", 0, 3000)
		h.tick()
	}
	first := -1
	seen := map[model.State]int{}
	// 断流 36s（本机实测最长的正常长思考）不能报卡住
	for i := 1; i <= 180; i++ {
		h.tick()
		st := h.target("reclaude").State
		seen[st]++
		if i <= 36 && (st == model.StateSuspect || st == model.StateStall) {
			t.Fatalf("断流第 %d 秒就报 %s：正常长思考实测最长 36s 无字节", i, st)
		}
		if st == model.StateStall && first < 0 {
			first = i
		}
	}
	if first < 0 || first > int(HookMinStall.Seconds()) {
		t.Fatalf("回包到一半断流：断流后第 %d 秒才报 STALL（-1 = 没报），要 %v 内；各状态秒数 %v", first, HookMinStall, seen)
	}
	h.want("reclaude", model.StateStall, model.LayerLine)
	// 收到 Stop：回合结束了，不再在飞，卡住按未恢复收尾
	h.hookEventApp("stop", "s1", "reclaude")
	h.tick()
	if tg := h.target("reclaude"); tg.State == model.StateStall || tg.State == model.StateSuspect || tg.Recovered {
		t.Fatalf("Stop 之后：%s recovered=%v", tg.State, tg.Recovered)
	}
}

// 探针 2：两个 mirasim 会话共用 launch.log。A 的回合结束（turn-result + Stop），B 还在飞、线路停了 120 秒。
func TestHookFlightSecondSessionStallsAfterSharedLogEnds(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c2", "api.mirasim.ai", "JP-1", 50002)
	h.tick()
	h.hookEventApp("prompt", "A", "Mirasim")
	appendTo(t, h.launch, "event=turn-start\n")
	h.tick()
	h.hookEventApp("prompt", "B", "Mirasim")
	appendTo(t, h.launch, "event=turn-start\n")
	h.mh.bump("c2", 20000, 0) // B 发出模型请求
	h.tick()
	h.mh.bump("c2", 0, 3000) // 回了一点
	h.tick()
	appendTo(t, h.launch, "event=turn-result\n")
	h.hookEventApp("stop", "A", "Mirasim")
	h.tick()
	first := -1
	for i := 1; i <= 120; i++ {
		h.tick()
		if h.target("mirasim").State == model.StateStall && first < 0 {
			first = i
		}
	}
	if first < 0 || first > int(HookMinStall.Seconds()) {
		t.Fatalf("B 卡住：A 结束后第 %d 秒才报 STALL（-1 = 没报），要 %v 内", first, HookMinStall)
	}
}

// 收到 Stop 后静默：回合结束，等多久都不报。
func TestHookStopThenSilenceNoAlarm(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.hookEventApp("prompt", "s1", "reclaude")
	h.mh.bump("c1", 20000, 0)
	h.tick()
	for i := 0; i < 5; i++ {
		h.mh.bump("c1", 0, 3000)
		h.tick()
	}
	h.hookEventApp("stop", "s1", "reclaude")
	for i := 0; i < 180; i++ {
		h.tick()
		if st := h.target("reclaude").State; st == model.StateSuspect || st == model.StateStall {
			t.Fatalf("Stop 之后第 %d 秒：%s", i+1, st)
		}
	}
	h.want("reclaude", model.StateIdle, model.LayerNone)
}

// 在飞期间工具在跑（Pre 到 Post）不算在等回包；Post 之后重新计时。字节一直在流就不报。
func TestHookFlightToolRunAndStreamingNoAlarm(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.hookEventApp("prompt", "s1", "reclaude")
	h.mh.bump("c1", 20000, 0)
	h.tick()
	for i := 0; i < 60; i++ { // 一直在流
		h.mh.bump("c1", 0, 2000)
		h.tick()
	}
	h.hookEventApp("pre", "s1", "reclaude")
	for i := 0; i < 90; i++ { // 本地工具跑 90 秒
		h.tick()
		if st := h.target("reclaude").State; st != model.StateBusy && st != model.StateIdle {
			t.Fatalf("工具在跑第 %d 秒：%s（工具在跑不算在等回包）", i+1, st)
		}
	}
	h.hookEventApp("post", "s1", "reclaude")
	h.mh.bump("c1", 20000, 0)
	h.tick()
	h.ticks(5)
	h.mh.bump("c1", 0, 2000)
	for i := 0; i < 20; i++ {
		h.mh.bump("c1", 0, 2000)
		h.tick()
		if st := h.target("reclaude").State; st != model.StateOK {
			t.Fatalf("工具结束后在流：第 %d 秒 %s", i+1, st)
		}
	}
	h.hookEventApp("stop", "s1", "reclaude")
	h.tick()
}

// 本机实测：Mirasim 会话的模型流量不走 relay.mirasim.ai，relay 上只有心跳（上下行常常分在相邻两秒）和小请求。
// 在飞期间目标线路上没发出过模型请求（一秒上行 ≥ HookRequestMin），hook 层不上膛，不报。
func TestHookFlightNotArmedWithoutModelRequest(t *testing.T) {
	h := newHarness(t)
	h.mh.add("relay", "relay.mirasim.ai", "JP-1", 50020)
	h.tick()
	h.hookEventApp("prompt", "s1", "Mirasim")
	for i := 1; i <= 300; i++ {
		switch i % 30 {
		case 0:
			h.mh.bump("relay", 863, 0)
		case 1:
			h.mh.bump("relay", 0, 658)
		case 15:
			h.mh.bump("relay", 1520, 458)
		}
		h.tick()
		if st := h.target("mirasim").State; st == model.StateSuspect || st == model.StateStall {
			t.Fatalf("第 %d 秒：线路上没有模型请求不该报，实际 %s（%s）", i, st, h.target("mirasim").Reason)
		}
	}
}

// 本机真卡住样本（09:23 断网，托盘账本 reclaude A 101s）：一条连接发了 1.2KB 请求再没回包，
// 两秒后另一条连接来了 5.5KB（那是它自己的回包），之后线路几乎全停。另一条连接的回包不能把这条的等待清掉。
func TestRealOutageSiblingReplyDoesNotClearPending(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.mh.add("c2", "misaka.route.reclaude.ai", "JP-1", 50002)
	h.tick()
	h.mh.bump("c1", 1224, 0)
	h.mh.bump("c2", 80, 0)
	h.tick()
	h.tick()
	h.mh.bump("c2", 0, 5508)
	h.tick()
	h.ticks(28)
	h.want("reclaude", model.StateStall, model.LayerLine) // 请求后 30s
}

// 本机实测：hook 带回合事件时，Mirasim 一个长回合里 launch.log 几分钟没有新帧、relay 上只有心跳，
// 回合层不该把它报成卡住（模型在不在等由 hook 层判）。
func TestTurnLayerCappedWhenHookHasRounds(t *testing.T) {
	h := newHarnessMirasimLog(t, `progress: 'event=turn-(first-output|result)', start: 'event=(launch|turn-first-output)( |$)', end: 'event=(turn-result|reject|once-exit)( |$)'`)
	h.mh.add("relay", "relay.mirasim.ai", "JP-1", 50020)
	h.tick()
	h.hookEventApp("prompt", "s1", "Mirasim")
	appendTo(t, h.launch, "event=launch\nevent=turn-first-output\n")
	h.tick()
	h.hookEventApp("stop", "s1", "Mirasim")
	for i := 1; i <= 180; i++ {
		if i%30 == 0 {
			h.mh.bump("relay", 863, 658)
		}
		h.tick()
		if st := h.target("mirasim").State; st == model.StateStall {
			t.Fatalf("第 %d 秒：回合层不该报卡住（%s）", i, h.target("mirasim").Reason)
		}
	}
}
