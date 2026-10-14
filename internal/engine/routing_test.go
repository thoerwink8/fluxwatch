package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// #27：目标按「流量走哪条线路」划分。默认结构：reclaude 收所有 Claude 会话（claude: all）、挂 launch.log 的回合；
// mirasim 只管云端中转 relay.mirasim.ai，不接 hook。连接的软件名：reclaude 网关发的是 reclaude，relay 是 Mirasim。
func newRoutedHarness(t *testing.T, claudeLine string) *harness {
	t.Helper()
	h := newHarness(t)
	h.e.opts.Procs = &mapProcs{apps: map[int]string{50001: "reclaude", 50002: "reclaude", 50020: "Mirasim"}}
	cfg := fmt.Sprintf(`defaults: { suspect: 10s, stall: 30s }
mihomo: { controller: %q }
targets:
  - name: reclaude
    label: ReClaude
%s    sources:
      - { type: mihomo, hosts: ["*.reclaude.ai"], apps: [reclaude] }
      - { type: logtail, path: %q, error: ' (429|50[0-9]) ' }
      - { type: logtail, path: %q, progress: 'event=turn-(first-output|result)', start: 'event=(launch|turn-first-output)( |$)', end: 'event=(turn-result|reject|once-exit)( |$)' }
  - name: mirasim
    label: Mirasim Cloud
    sources:
      - { type: mihomo, hosts: ["*.mirasim.ai"], apps: [Mirasim] }
`, strings.TrimPrefix(h.srv.URL, "http://"), claudeLine, h.daemon, h.launch)
	p := filepath.Join(h.dir, "targets.yaml")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	future := h.now.Add(time.Hour)
	os.Chtimes(p, future, future)
	h.mh.add("rc", "misaka.route.reclaude.ai", "HK-5", 50001)
	h.mh.add("relay", "relay.mirasim.ai", "HK-5", 50020)
	h.tick()
	return h
}

// Mirasim 起的会话跑工具：等回包的 reclaude 线路是本地忙，mirasim（relay）照常判。
func TestRoutingMirasimSessionToolMakesReclaudeBusy(t *testing.T) {
	h := newRoutedHarness(t, "    claude: all\n")
	h.mh.bump("rc", 20000, 0)
	h.mh.bump("relay", 4000, 0)
	h.tick()
	h.hookEventApp("pre", "m1", "Mirasim")
	sawStall := false
	for i := 0; i < 40; i++ {
		h.tick()
		if st := h.target("reclaude").State; st != model.StateBusy {
			t.Fatalf("第 %d 秒：Mirasim 会话在跑工具，reclaude 要 busy，实际 %s", i+1, st)
		}
		switch h.target("mirasim").State {
		case model.StateBusy:
			t.Fatalf("第 %d 秒：mirasim 不接 hook，不该 busy", i+1)
		case model.StateStall:
			sawStall = true
		}
	}
	if !sawStall {
		t.Fatalf("relay 请求不回包，mirasim 照样要报 STALL，此刻 %s", h.target("mirasim").State)
	}
}

// Mirasim 起的会话在 reclaude 线路上断流（回包到一半）：75 秒内报 reclaude STALL；mirasim 不受影响。
func TestRoutingMirasimSessionMidStreamStallOnReclaude(t *testing.T) {
	h := newRoutedHarness(t, "    claude: all\n")
	h.hookEventApp("prompt", "m1", "Mirasim")
	appendTo(t, h.launch, "event=launch\nevent=turn-first-output\n")
	h.mh.bump("rc", 30000, 0)
	h.tick()
	for i := 0; i < 5; i++ {
		h.mh.bump("rc", 0, 3000)
		h.tick()
	}
	first := -1
	for i := 1; i <= 120; i++ {
		if i%30 == 0 {
			h.mh.bump("relay", 863, 658) // relay 心跳照常
		}
		h.tick()
		if h.target("reclaude").State == model.StateStall && first < 0 {
			first = i
		}
		if st := h.target("mirasim").State; st != model.StateIdle {
			t.Fatalf("第 %d 秒：mirasim 只看 relay，不该是 %s", i, st)
		}
	}
	if first < 0 || first > int(HookMinStall.Seconds()) {
		t.Fatalf("reclaude 线路断流：第 %d 秒才报 STALL（-1 = 没报），要 %v 内", first, HookMinStall)
	}
	h.want("reclaude", model.StateStall, model.LayerLine)
}

// relay.mirasim.ai 的请求不回包：照样报 mirasim STALL（字节层，不靠 hook）。
func TestRoutingRelayRequestNoReplyStallsMirasim(t *testing.T) {
	h := newRoutedHarness(t, "    claude: all\n")
	h.mh.bump("relay", 4000, 0)
	h.tick()
	h.ticks(30)
	h.want("mirasim", model.StateStall, model.LayerLine)
	h.want("reclaude", model.StateIdle, model.LayerNone)
}

// 老配置（没有目标写 claude）：引擎照常跑，hook 不归任何目标——不 busy、回合层只报慢、字节层照常。
func TestRoutingOldConfigWithoutClaudeField(t *testing.T) {
	h := newRoutedHarness(t, "")
	h.hookEventApp("prompt", "m1", "Mirasim")
	h.hookEventApp("pre", "m1", "Mirasim")
	appendTo(t, h.launch, "event=launch\nevent=turn-first-output\n")
	h.mh.bump("rc", 0, 5000)
	h.tick()
	for i := 0; i < 120; i++ {
		h.tick()
		tg := h.target("reclaude")
		if tg.State == model.StateBusy || tg.State == model.StateStall {
			t.Fatalf("第 %d 秒：老配置不接 hook、回合层只报慢，实际 %s（%s）", i+1, tg.State, tg.Reason)
		}
	}
	h.hookEventApp("post", "m1", "Mirasim")
	h.mh.bump("rc", 20000, 0) // 字节层照常：请求不回包报卡住
	h.tick()
	h.ticks(30)
	h.want("reclaude", model.StateStall, model.LayerLine)
}

// 本机重放的真实样子：hook 带回合事件、会话在飞，reclaude 网关一条连接只往上传（几十 KB、不回包），
// 回复走另一条连接。卡没卡只由 hook 层判：回复在流就不报，连「慢」也不报。
func TestRoutingHookedTargetIgnoresUploadOnlyConn(t *testing.T) {
	h := newRoutedHarness(t, "    claude: all\n")
	h.mh.add("rc2", "misaka.route.reclaude.ai", "HK-5", 50002)
	h.hookEventApp("prompt", "m1", "Mirasim")
	h.mh.bump("rc", 30000, 0)
	h.tick()
	for i := 0; i < 180; i++ {
		if i%5 == 0 {
			h.mh.bump("rc", 60000, 0) // 只上传
		}
		if i%3 == 0 {
			h.mh.bump("rc2", 200, 4000) // 回复在另一条连接
		}
		h.tick()
		if st := h.target("reclaude").State; st == model.StateSuspect || st == model.StateStall {
			t.Fatalf("第 %d 秒：回复在流，不该报 %s（%s）", i+1, st, h.target("reclaude").Reason)
		}
	}
}

// 本机 09:22 真断网的样子：hook 带回合事件（别的会话发过），但这次在等的会话 hook 不知道（装回合事件之前起的），
// reclaude 一条连接发了 1.2KB 请求再没回包。hook 层没在飞，字节层照常判：30 秒报 STALL。
func TestRoutingByteLayerStillJudgesWhenHookHasNoFlight(t *testing.T) {
	h := newRoutedHarness(t, "    claude: all\n")
	h.hookEventApp("prompt", "other", "Mirasim")
	h.hookEventApp("stop", "other", "Mirasim")
	h.tick()
	h.mh.bump("rc", 1224, 0)
	h.tick()
	h.ticks(30)
	h.want("reclaude", model.StateStall, model.LayerLine)
}
