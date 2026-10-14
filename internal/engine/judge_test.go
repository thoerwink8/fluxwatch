package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/ledger"
	"github.com/thoerwink8/fluxwatch/internal/model"
)

// 评审（v0.1 发版前）里假时钟探针转成的用例：#18 的 M3、M6、M4、M5。

// ledgerEntries 读出账本里的全部行。
func (h *harness) ledgerEntries() []ledger.Entry {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, "ledger.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		h.t.Fatal(err)
	}
	var out []ledger.Entry
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var e ledger.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			h.t.Fatalf("账本行坏了：%q：%v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func (h *harness) dropConn(id string) {
	h.mh.mu.Lock()
	defer h.mh.mu.Unlock()
	delete(h.mh.conns, id)
	var order []string
	for _, o := range h.mh.order {
		if o != id {
			order = append(order, o)
		}
	}
	h.mh.order = order
}

// M3：c1 发了请求一直没回包，同目标另一条连接 c2 每 25 秒一次 relay 心跳（上下行同一秒的小包）。
// 心跳不是 c1 的回包，也不是回包：30 秒内要报 STALL。
func TestHeartbeatOnOtherConnDoesNotClearPendingRequest(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "relay.mirasim.ai", "JP-1", 50001)
	h.mh.add("c2", "relay.mirasim.ai", "JP-1", 50002)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	first := -1
	for i := 1; i <= 120; i++ {
		if i%25 == 0 {
			h.mh.bump("c2", 863, 658)
		}
		h.tick()
		if h.target("mirasim").State == model.StateStall && first < 0 {
			first = i - 1 // 第 1 轮就是发请求那一秒（等了 0s）
		}
	}
	if first < 0 || first > 30 {
		t.Fatalf("c1 请求不回包、c2 只有心跳：等了 %ds 才报 STALL（-1 = 没报），要 30 秒内", first)
	}
	h.want("mirasim", model.StateStall, model.LayerLine)
}

// M3：发请求那条连接自己的心跳（同一秒上下行都是小包）也不算回包；真回包（哪怕小）照样算。
func TestPingPongOnPendingConnIsNotAReply(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.ticks(19)
	h.mh.bump("c1", 863, 658) // 20s：同一条连接上的 ping-pong
	h.ticks(11)
	h.want("reclaude", model.StateStall, model.LayerLine) // 30s
	h.mh.bump("c1", 0, 300)                               // 真回包
	h.tick()
	h.want("reclaude", model.StateOK, model.LayerNone)
}

// M6：请求发出后连接没了，再也没有新请求：STALL 后 60 秒内转 IDLE，账本这一条按未恢复收尾、时长 ≤ 150s。
func TestAbandonedRequestEndsUnrecovered(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.dropConn("c1")
	stallAt, idleAt := -1, -1
	for i := 1; i <= 3600; i++ {
		h.tick()
		st := h.target("reclaude").State
		if st == model.StateStall && stallAt < 0 {
			stallAt = i
		}
		if st == model.StateIdle && stallAt >= 0 && idleAt < 0 {
			idleAt = i
		}
		if h.target("reclaude").Recovered {
			t.Fatalf("第 %d 秒：连接没了不算恢复", i)
		}
	}
	if stallAt < 0 || idleAt < 0 || idleAt-stallAt > 60 {
		t.Fatalf("STALL@%d IDLE@%d：要 STALL 后 60 秒内转 IDLE", stallAt, idleAt)
	}
	h.want("reclaude", model.StateIdle, model.LayerNone)
	es := h.ledgerEntries()
	if len(es) != 1 {
		t.Fatalf("账本应当一行，有 %d：%+v", len(es), es)
	}
	if es[0].RecoveredAt != nil || es[0].Seconds > 150 {
		t.Fatalf("账本这一条应当未恢复、≤150s：%+v", es[0])
	}
}

// M6：连接没了以后 60 秒内来了新请求（客户端重试），新请求照常判；重试回包了才算恢复。
func TestRetryAfterConnGoneKeepsStallUntilReply(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.dropConn("c1")
	h.ticks(34)
	h.want("reclaude", model.StateStall, model.LayerLine)
	h.mh.add("c2", "misaka.route.reclaude.ai", "JP-1", 50002)
	h.mh.bump("c2", 4000, 0) // 重试：新请求不是进展，卡住不因为它结束
	h.tick()
	h.want("reclaude", model.StateStall, model.LayerLine)
	h.ticks(5)
	h.want("reclaude", model.StateStall, model.LayerLine)
	h.mh.bump("c2", 0, 9000)
	h.tick()
	tg := h.want("reclaude", model.StateOK, model.LayerNone)
	if !tg.Recovered {
		t.Fatal("重试回包是真恢复，要带 Recovered")
	}
	es := h.ledgerEntries()
	if len(es) != 1 || es[0].RecoveredAt == nil {
		t.Fatalf("账本：%+v", es)
	}
}

// M4：mirasim 回合开着、真卡住（没有新帧），只有 relay 每 30 秒一次心跳。
// 4 分钟里只进一次 STALL、之后一直 STALL，没有「恢复」。
func TestTurnStallWithHeartbeatDoesNotFlap(t *testing.T) {
	h := newHarnessMirasimLog(t, `progress: 'event=turn-(first-output|result)', start: 'event=(launch|turn-first-output)( |$)', end: 'event=(turn-result|reject|once-exit)( |$)'`)
	h.mh.add("c2", "relay.mirasim.ai", "JP-1", 50002)
	h.tick()
	appendTo(t, h.launch, "event=launch\n")
	h.mh.bump("c2", 4000, 0)
	h.tick()
	appendTo(t, h.launch, "event=turn-first-output\n")
	h.mh.bump("c2", 0, 5000)
	h.tick()
	var seq []string
	prev := model.State("")
	stalls, recovered := 0, 0
	for i := 1; i <= 240; i++ {
		if i%30 == 0 {
			h.mh.bump("c2", 863, 658)
		}
		h.tick()
		tg := h.target("mirasim")
		if tg.State != prev {
			seq = append(seq, string(tg.State)+"@"+time.Duration(i*int(time.Second)).String())
			if tg.State == model.StateStall {
				stalls++
			}
			prev = tg.State
		}
		if tg.Recovered {
			recovered++
		}
	}
	if stalls != 1 || recovered != 0 || prev != model.StateStall {
		t.Fatalf("回合卡住 + 心跳：进 STALL %d 次、恢复 %d 次，要 1 / 0；跳变 %v", stalls, recovered, seq)
	}
	if es := h.ledgerEntries(); len(es) != 0 {
		t.Fatalf("还卡着，账本不该收尾：%+v", es)
	}
}

// M4：线路真在动（窗口里累计够量的下行）而日志没新帧 → THINKING；只有心跳 → 不算线路在动。
func TestLineMovingNeedsRealDownlink(t *testing.T) {
	h := newHarness(t)
	h.mh.add("m1", "api.mirasim.ai", "US-2", 50010)
	h.tick()
	appendTo(t, h.launch, "event=turn-start\n")
	h.mh.bump("m1", 4000, 0)
	h.tick()
	appendTo(t, h.launch, "event=turn-first-output\n")
	h.mh.bump("m1", 0, 300)
	h.tick()
	for i := 0; i < 15; i++ {
		if i%5 == 0 {
			h.mh.bump("m1", 300, 300) // 小包 ping-pong
		}
		h.tick()
	}
	if st := h.target("mirasim").State; st != model.StateSuspect {
		t.Fatalf("只有心跳不算线路在动：15s 应当 SUSPECT，实际 %s", st)
	}
}

// M5：在飞时睡了 2 小时。唤醒后 30 秒内不报；宽限后新请求不回包照样 30 秒内 STALL。
func TestSleepWakeGraceThenStallsAgain(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.ticks(3)
	h.now = h.now.Add(2 * time.Hour)
	for i := 0; i < 30; i++ {
		s := h.tick()
		if tg := h.target("reclaude"); tg.State == model.StateSuspect || tg.State == model.StateStall || s.Alerting != 0 {
			t.Fatalf("唤醒后第 %d 秒：%s stuck=%.0f（%s）", i+1, tg.State, tg.StuckFor, tg.Reason)
		}
	}
	h.ticks(5)
	h.mh.bump("c1", 4000, 0) // 宽限之后的新请求，不回包
	first := -1
	for i := 1; i <= 40; i++ {
		h.tick()
		if h.target("reclaude").State == model.StateStall && first < 0 {
			first = i - 1 // 第 1 轮就是发请求那一秒（等了 0s）
		}
	}
	if first < 0 || first > 30 {
		t.Fatalf("宽限后新请求不回包：等了 %ds 才报 STALL，要 30 秒内", first)
	}
}

// M5：mirasim 回合开着时睡了 2 小时，唤醒后线路在动：不因为睡眠那段报卡住。
func TestSleepWakeTurnOpenNoAlarm(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c2", "api.mirasim.ai", "JP-1", 50002)
	h.tick()
	appendTo(t, h.launch, "event=turn-start\n")
	h.tick()
	h.now = h.now.Add(2 * time.Hour)
	h.mh.bump("c2", 0, 5000)
	for i := 0; i < 29; i++ {
		h.tick()
		if tg := h.target("mirasim"); tg.State == model.StateSuspect || tg.State == model.StateStall {
			t.Fatalf("唤醒后第 %d 秒：%s stuck=%.0f（%s）", i+1, tg.State, tg.StuckFor, tg.Reason)
		}
	}
}

// M5：卡住时睡了 2 小时：这一条按未恢复收尾，时长截到睡前，不含睡眠。
func TestSleepWhileStalledLedgerExcludesSleep(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.ticks(39)
	h.want("reclaude", model.StateStall, model.LayerLine) // 40s
	h.now = h.now.Add(2 * time.Hour)
	h.tick()
	if tg := h.target("reclaude"); tg.State == model.StateStall || tg.Recovered {
		t.Fatalf("唤醒第一轮：%s recovered=%v", tg.State, tg.Recovered)
	}
	es := h.ledgerEntries()
	if len(es) != 1 {
		t.Fatalf("账本应当一行：%+v", es)
	}
	if es[0].RecoveredAt != nil || es[0].Seconds > 45 {
		t.Fatalf("睡前的卡住应当未恢复、时长不含睡眠：%+v", es[0])
	}
}
