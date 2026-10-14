package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/config"
	"github.com/thoerwink8/fluxwatch/internal/ledger"
	"github.com/thoerwink8/fluxwatch/internal/mihomo"
	"github.com/thoerwink8/fluxwatch/internal/model"
	"github.com/thoerwink8/fluxwatch/internal/procattr"
)

// fakeMihomo 是假的 mihomo 控制接口：/version 和 /connections（chunked）。
type fakeMihomo struct {
	mu    sync.Mutex
	conns map[string]*mihomo.Connection
	order []string
	fail  bool
}

func (f *fakeMihomo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case "/version":
		fmt.Fprint(w, `{"meta":true,"version":"v1.19.14"}`)
	case "/connections":
		var list []mihomo.Connection
		for _, id := range f.order {
			list = append(list, *f.conns[id])
		}
		b, _ := json.Marshal(mihomo.Connections{Connections: list})
		// 分两块写出，模拟 chunked
		w.Write(b[:len(b)/2])
		w.(http.Flusher).Flush()
		w.Write(b[len(b)/2:])
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeMihomo) add(id, host, node string, port int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conns == nil {
		f.conns = map[string]*mihomo.Connection{}
	}
	c := &mihomo.Connection{ID: id, Chains: []string{node, "Proxy"}, Start: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)}
	c.Metadata.Host = host
	c.Metadata.SourcePort = fmt.Sprint(port)
	f.conns[id] = c
	f.order = append(f.order, id)
}

func (f *fakeMihomo) bump(id string, up, down int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns[id].Upload += up
	f.conns[id].Download += down
}

type fakeProcs struct{}

func (fakeProcs) Lookup(ports []int) (map[int]procattr.Proc, error) {
	m := map[int]procattr.Proc{}
	for _, p := range ports {
		m[p] = procattr.Proc{PID: 1000 + p, App: "Mirasim"}
	}
	return m, nil
}

// mapProcs 按端口给软件名；fail 时整轮反查失败。
type mapProcs struct {
	mu   sync.Mutex
	apps map[int]string
	fail bool
}

func (m *mapProcs) Lookup(ports []int) (map[int]procattr.Proc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return nil, fmt.Errorf("lsof 跑不起来")
	}
	out := map[int]procattr.Proc{}
	for _, p := range ports {
		if a, ok := m.apps[p]; ok {
			out[p] = procattr.Proc{PID: 1000 + p, App: a}
		}
	}
	return out, nil
}

type harness struct {
	t        *testing.T
	dir      string
	now      time.Time
	mh       *fakeMihomo
	srv      *httptest.Server
	e        *Engine
	daemon   string
	launch   string
	hook     string
	settings string
	last     model.Snapshot
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessMirasimLog(t, `progress: 'event=turn-(first-output|result)', start: 'event=turn-start', end: 'event=turn-result'`)
}

// newHarnessMirasimLog 同 newHarness，mirasim 的 logtail 用给定的正则（path 之外的那几项）。
func newHarnessMirasimLog(t *testing.T, mirasimLog string) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), now: time.Date(2026, 10, 14, 10, 0, 0, 0, time.Local), mh: &fakeMihomo{}}
	h.srv = httptest.NewServer(h.mh)
	t.Cleanup(h.srv.Close)
	h.daemon = filepath.Join(h.dir, "daemon.log")
	h.launch = filepath.Join(h.dir, "launch.log")
	h.hook = filepath.Join(h.dir, "hook.jsonl")
	h.settings = filepath.Join(h.dir, "settings.json")
	// hook.jsonl 在：大多数用例按「装了 hook」跑；没装 hook 的用例自己删掉它
	for _, p := range []string{h.daemon, h.launch, h.hook} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf(`defaults: { suspect: 10s, stall: 30s }
mihomo: { controller: %q }
targets:
  - name: reclaude
    label: ReClaude
    sources:
      - { type: mihomo, hosts: ["*.reclaude.ai"] }
      - { type: logtail, path: %q, error: ' (429|50[0-9]) ' }
  - name: mirasim
    label: Mirasim Cloud
    sources:
      - { type: mihomo, hosts: ["*.mirasim.ai"] }
      - { type: logtail, path: %q, %s }
`, strings.TrimPrefix(h.srv.URL, "http://"), h.daemon, h.launch, mirasimLog)
	if err := os.WriteFile(filepath.Join(h.dir, "targets.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{
		ConfigPath:   filepath.Join(h.dir, "targets.yaml"),
		LedgerPath:   filepath.Join(h.dir, "ledger.jsonl"),
		HookPath:     h.hook,
		SettingsPath: h.settings,
		Now:          func() time.Time { return h.now },
		Procs:        fakeProcs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	h.e = e
	return h
}

// tick 走一秒并跑一轮。
func (h *harness) tick() model.Snapshot {
	h.now = h.now.Add(time.Second)
	h.last = h.e.Tick()
	return h.last
}

func (h *harness) ticks(n int) model.Snapshot {
	for i := 0; i < n; i++ {
		h.tick()
	}
	return h.last
}

func (h *harness) target(name string) model.Target {
	h.t.Helper()
	for _, t := range h.last.Targets {
		if t.Name == name {
			return t
		}
	}
	h.t.Fatalf("快照里没有 %s：%+v", name, h.last.Targets)
	return model.Target{}
}

func (h *harness) want(name string, st model.State, layer model.Layer) model.Target {
	h.t.Helper()
	tg := h.target(name)
	if tg.State != st || tg.Layer != layer {
		h.t.Fatalf("%s 此刻 %s/%q（%s），要 %s/%q", name, tg.State, tg.Layer, tg.Reason, st, layer)
	}
	return tg
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func (h *harness) hookEvent(ev string) {
	b, _ := json.Marshal(map[string]any{"ts": h.now, "event": ev, "tool": "Bash", "session": "s1"})
	appendTo(h.t, h.hook, string(b)+"\n")
}

func TestIdleConnectionsWithZeroBytesNeverAlarm(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.mh.add("c2", "api.mirasim.ai", "JP-1", 50002)
	h.mh.bump("c1", 5000, 90000) // 建基线前已有的累计字节不算
	for i := 0; i < 180; i++ {
		s := h.tick()
		if s.Alerting != 0 || s.Worst != model.StateIdle {
			t.Fatalf("第 %d 秒：空闲连接误报 worst=%s alerting=%d", i, s.Worst, s.Alerting)
		}
	}
	tg := h.want("reclaude", model.StateIdle, model.LayerNone)
	if len(tg.Conns) != 1 || tg.Conns[0].App != "Mirasim" || tg.Conns[0].IdleFor <= 0 {
		t.Fatalf("连接 / 进程归属不对：%+v", tg.Conns)
	}
}

func TestOKSuspectStallLayerARecoverIdle(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick() // 基线
	h.want("reclaude", model.StateIdle, model.LayerNone)

	h.mh.bump("c1", 4000, 0) // 发请求，没回包
	h.tick()
	h.want("reclaude", model.StateOK, model.LayerNone)
	h.ticks(9)
	t.Log(h.e.Diagnose("reclaude"), h.target("reclaude"))
	h.want("reclaude", model.StateOK, model.LayerNone) // 9s
	h.tick()
	h.want("reclaude", model.StateSuspect, model.LayerLine) // 10s
	if h.last.Alerting != 1 || h.last.Worst != model.StateSuspect {
		t.Fatalf("worst=%s alerting=%d", h.last.Worst, h.last.Alerting)
	}
	h.ticks(19)
	h.want("reclaude", model.StateSuspect, model.LayerLine) // 29s
	h.tick()
	tg := h.want("reclaude", model.StateStall, model.LayerLine) // 30s
	if !strings.Contains(tg.Reason, "JP-1") || tg.StuckFor != 30 || tg.Node != "JP-1" || tg.TodayCount != 1 {
		t.Fatalf("STALL 细节不对：%+v", tg)
	}
	h.ticks(5)
	h.want("reclaude", model.StateStall, model.LayerLine)

	h.mh.bump("c1", 0, 800) // 回包了
	h.tick()
	h.want("reclaude", model.StateOK, model.LayerNone)
	if got := h.target("reclaude").TodayCount; got != 1 {
		t.Fatalf("恢复后今天次数 = %d，要 1（账本）", got)
	}
	l, err := ledger.Open(filepath.Join(h.dir, "ledger.jsonl"), h.now)
	if err != nil {
		t.Fatal(err)
	}
	if l.TodayCount(h.now, "reclaude", "JP-1") != 1 {
		t.Fatal("账本里应当有一行")
	}
	b, _ := os.ReadFile(filepath.Join(h.dir, "ledger.jsonl"))
	if !strings.Contains(string(b), `"recoveredAt"`) || !strings.Contains(string(b), `"layer":"A"`) {
		t.Fatalf("账本行：%s", b)
	}

	h.ticks(59)
	h.want("reclaude", model.StateOK, model.LayerNone)
	h.tick()
	h.want("reclaude", model.StateIdle, model.LayerNone) // 60s 没有动静
}

func TestStallLayerBFromLogStatusCodes(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.ticks(5)
	appendTo(t, h.daemon, "2026-10-14 10:00:06 tunnel error: upstream 503 bad gateway\n2026-10-14 10:00:06 upstream 503 again\n")
	h.ticks(5) // 10s
	h.want("reclaude", model.StateSuspect, model.LayerUpstream)
	h.ticks(20)
	tg := h.want("reclaude", model.StateStall, model.LayerUpstream)
	if !strings.Contains(tg.Reason, "daemon.log 503 ×2") {
		t.Fatalf("reason = %q", tg.Reason)
	}
}

func TestStallLayerCFromQuotaHint(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.tick()
	appendTo(t, h.daemon, "account quota exhausted, switching account\n")
	h.ticks(30)
	tg := h.want("reclaude", model.StateStall, model.LayerAccount)
	if !strings.Contains(tg.Reason, "配额") {
		t.Fatalf("reason = %q", tg.Reason)
	}
}

func TestBusySuppressesAlarmAndResumesTiming(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.ticks(4) // 等了 4s
	h.hookEvent("pre")
	for i := 0; i < 90; i++ {
		s := h.tick()
		if tg := h.target("reclaude"); tg.State != model.StateBusy || s.Alerting != 0 {
			t.Fatalf("工具在跑第 %d 秒：%s alerting=%d", i, tg.State, s.Alerting)
		}
	}
	// 没在活动的目标不被 busy 带走
	h.want("mirasim", model.StateIdle, model.LayerNone)
	h.hookEvent("post")
	h.tick()
	h.want("reclaude", model.StateOK, model.LayerNone) // 原来的 4s + 1s
	h.ticks(4)
	h.want("reclaude", model.StateOK, model.LayerNone)
	h.tick()
	h.want("reclaude", model.StateSuspect, model.LayerLine) // 接着原来的计时到 10s
}

func TestThinkingWhileLineMovesThenStallWhenLineStops(t *testing.T) {
	h := newHarness(t)
	h.mh.add("m1", "api.mirasim.ai", "US-2", 50010)
	h.tick()
	appendTo(t, h.launch, "pid=1 event=turn-start generation=1\n")
	h.mh.bump("m1", 4000, 0)
	h.tick()
	h.mh.bump("m1", 0, 300)
	appendTo(t, h.launch, "pid=1 event=turn-first-output generation=1\n")
	h.tick()
	h.want("mirasim", model.StateOK, model.LayerNone)
	// 回合还没结束，日志迟迟没新帧，但线路一直在动
	for i := 0; i < 40; i++ {
		h.mh.bump("m1", 0, 200)
		h.tick()
	}
	h.want("mirasim", model.StateThinking, model.LayerNone)
	if h.last.Alerting != 0 {
		t.Fatalf("THINKING 不算报警：%d", h.last.Alerting)
	}
	// 线路也停了：超过 suspect 没字节 → 卡住，判 A
	h.ticks(10)
	h.want("mirasim", model.StateStall, model.LayerLine)
	// 线路又动了但还是没新帧：不是真进展，停在 STALL、不算恢复，是同一次卡住（#18 M4：以前回 THINKING，来回翻）
	h.mh.bump("m1", 0, 200)
	h.tick()
	if tg := h.want("mirasim", model.StateStall, model.LayerLine); tg.Recovered {
		t.Fatal("线路动了一下不算恢复")
	}
	h.ticks(10)
	h.want("mirasim", model.StateStall, model.LayerLine)
	if got := h.target("mirasim").TodayCount; got != 1 {
		t.Fatalf("同一次卡住来回跳，今天次数 = %d，要 1", got)
	}
	appendTo(t, h.launch, "pid=1 event=turn-result generation=1 ms=50000\n")
	h.tick()
	h.want("mirasim", model.StateOK, model.LayerNone)
	b, _ := os.ReadFile(filepath.Join(h.dir, "ledger.jsonl"))
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Fatalf("账本应当只有一行，有 %d 行", n)
	}
}

// 真跑时采到的 relay 节奏：每 30 秒一次心跳（上下行同一秒），以及「发请求、下一秒回包」。
// 配了 progress 正则的目标也不能因为日志没新帧就误报。
func TestHeartbeatAloneStaysIdle(t *testing.T) {
	h := newHarness(t)
	h.mh.add("relay", "relay.mirasim.ai", "JP-1", 50020)
	h.tick()
	for i := 0; i < 300; i++ {
		if i%30 == 0 {
			h.mh.bump("relay", 863, 658)
		}
		h.tick()
		h.want("mirasim", model.StateIdle, model.LayerNone)
	}
}

func TestHeartbeatAndQuickRepliesOnProgressLogTargetDoNotAlarm(t *testing.T) {
	h := newHarness(t)
	h.mh.add("relay", "relay.mirasim.ai", "JP-1", 50020)
	h.mh.add("auth", "auth.mirasim.ai", "JP-1", 50021)
	h.tick()
	for i := 0; i < 300; i++ {
		switch {
		case i%30 == 0:
			h.mh.bump("relay", 863, 658)
		case i%45 == 0:
			h.mh.bump("auth", 1476, 0)
		case i%45 == 1:
			h.mh.bump("auth", 126, 4246)
		}
		if s := h.tick(); s.Alerting != 0 {
			t.Fatalf("第 %d 秒误报：%+v", i, h.target("mirasim"))
		}
		if st := h.target("mirasim").State; st != model.StateOK && st != model.StateIdle {
			t.Fatalf("第 %d 秒：%s", i, st)
		}
	}
}

func TestUnknownWhenMihomoUnreadableOrLogMissing(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.mh.mu.Lock()
	h.mh.fail = true
	h.mh.mu.Unlock()
	s := h.tick()
	for _, name := range []string{"reclaude", "mirasim"} {
		tg := h.want(name, model.StateUnknown, model.LayerNone)
		if !strings.Contains(tg.Reason, "mihomo") {
			t.Fatalf("%s reason = %q", name, tg.Reason)
		}
	}
	if s.Worst != model.StateUnknown || s.Alerting != 2 || len(s.SourceErr) == 0 {
		t.Fatalf("worst=%s alerting=%d err=%v", s.Worst, s.Alerting, s.SourceErr)
	}
	h.mh.mu.Lock()
	h.mh.fail = false
	h.mh.mu.Unlock()
	h.tick()
	h.want("reclaude", model.StateIdle, model.LayerNone)

	if err := os.Remove(h.daemon); err != nil {
		t.Fatalf("logtail 不该占着日志文件：%v", err)
	}
	h.tick()
	h.want("mirasim", model.StateIdle, model.LayerNone)
	// 日志不存在：只记一条 sourceErr，目标照 mihomo 判，不整体 UNKNOWN（#8）
	h.want("reclaude", model.StateIdle, model.LayerNone)
	if !strings.Contains(strings.Join(h.last.SourceErr, "|"), "reclaude：日志 daemon.log 不存在") {
		t.Fatalf("缺日志要记进 sourceErr：%v", h.last.SourceErr)
	}
	h.mh.bump("c1", 4000, 0) // 发请求不回包：照 mihomo 照样能判卡住
	h.tick()
	h.ticks(30)
	h.want("reclaude", model.StateStall, model.LayerLine)
	// 日志又建出来了：从头读，里面的 503 能用上
	h.mh.bump("c1", 0, 800)
	h.tick()
	appendTo(t, h.daemon, "upstream 503 bad gateway\n")
	h.tick()
	if strings.Contains(strings.Join(h.last.SourceErr, "|"), "daemon.log") {
		t.Fatalf("日志回来了 sourceErr 应当消失：%v", h.last.SourceErr)
	}
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.ticks(10)
	h.want("reclaude", model.StateSuspect, model.LayerUpstream)

	// 整个 mihomo 关掉（连不上）也一样
	h.srv.Close()
	h.tick()
	h.want("mirasim", model.StateUnknown, model.LayerNone)
}

func TestAdaptiveThresholdFollowsNodeP95(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "SLOW-1", 50001)
	h.tick()
	// 这个节点上发请求后 4 秒回包，攒够在飞期间的间隔；两次请求之间空闲 20 秒（不算进 p95）
	for i := 0; i < 30; i++ {
		h.mh.bump("c1", 2000, 0)
		h.tick()
		h.ticks(3)
		h.mh.bump("c1", 0, 500)
		h.tick()
		h.ticks(20)
	}
	h.want("reclaude", model.StateOK, model.LayerNone)
	h.mh.bump("c1", 2000, 0)
	h.tick()
	h.ticks(15) // 等了 16s：默认 10s 早该可疑，p95=4s → suspect 20s
	h.want("reclaude", model.StateOK, model.LayerNone)
	h.ticks(5)
	h.want("reclaude", model.StateSuspect, model.LayerLine) // 20s
	h.ticks(20)
	h.want("reclaude", model.StateStall, model.LayerLine) // 40s
}

func TestAddRemoveTargetDiscoverAndSubscribe(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.mh.add("c2", "api.example.com", "US-2", 50002)
	h.mh.add("c3", "cdn.example.com", "US-2", 50003)
	h.mh.bump("c2", 100, 7000)
	ch, cancel := h.e.Subscribe()
	defer cancel()
	h.tick()
	select {
	case s := <-ch:
		if len(s.Targets) != 2 {
			t.Fatalf("订阅拿到 %d 个目标", len(s.Targets))
		}
	default:
		t.Fatal("Tick 之后订阅应当收到一份")
	}

	ds, err := h.e.Discover()
	if err != nil {
		t.Fatal(err)
	}
	byHost := map[string]model.Discovered{}
	for _, d := range ds {
		byHost[d.Host] = d
	}
	if d := byHost["misaka.route.reclaude.ai"]; !d.Watched || d.Suggest != "*.reclaude.ai" || d.Apps[0] != "Mirasim" {
		t.Fatalf("reclaude 发现行：%+v", d)
	}
	if d := byHost["api.example.com"]; d.Watched || d.Suggest != "*.example.com" || d.Down != 7000 || d.Node != "US-2" {
		t.Fatalf("example 发现行：%+v", d)
	}
	if ds[0].Host != "api.example.com" {
		t.Fatalf("应当按下行排：%+v", ds[0])
	}

	if err := h.e.AddTarget("Example API", "*.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := h.e.AddTarget("again", "*.example.com"); err == nil {
		t.Fatal("重复的通配应当拒绝")
	}
	b, _ := os.ReadFile(filepath.Join(h.dir, "targets.yaml"))
	if !strings.Contains(string(b), "example-api") || !strings.Contains(string(b), "*.example.com") {
		t.Fatalf("没写回文件：%s", b)
	}
	h.tick()
	tg := h.want("example-api", model.StateIdle, model.LayerNone)
	if tg.Label != "Example API" || len(tg.Conns) != 2 || tg.LogPath != "" {
		t.Fatalf("新目标：%+v", tg)
	}
	if h.target("reclaude").LogPath != h.daemon {
		t.Fatalf("LogPath = %q", h.target("reclaude").LogPath)
	}
	ds, _ = h.e.Discover()
	for _, d := range ds {
		if strings.HasSuffix(d.Host, "example.com") && !d.Watched {
			t.Fatalf("加入后应当标已盯：%+v", d)
		}
	}
	if !strings.Contains(h.e.Diagnose("reclaude"), "ReClaude") {
		t.Fatal("诊断文本应当带目标名")
	}

	if err := h.e.RemoveTarget("example-api"); err != nil {
		t.Fatal(err)
	}
	h.tick()
	if len(h.last.Targets) != 2 {
		t.Fatalf("删掉后还有 %d 个目标", len(h.last.Targets))
	}
}

func TestHotReloadAndSettingsOverride(t *testing.T) {
	h := newHarness(t)
	h.tick()
	// settings.json 手填地址优先：先把 targets.yaml 的 controller 改坏
	cfgPath := filepath.Join(h.dir, "targets.yaml")
	b, _ := os.ReadFile(cfgPath)
	addr := strings.TrimPrefix(h.srv.URL, "http://")
	bad := strings.Replace(string(b), addr, "127.0.0.1:1", 1)
	bad = strings.Replace(bad, "suspect: 10s", "suspect: 5s", 1)
	if err := os.WriteFile(cfgPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	future := h.now.Add(time.Hour)
	os.Chtimes(cfgPath, future, future)
	h.tick()
	h.want("reclaude", model.StateUnknown, model.LayerNone) // 热加载生效：控制器连不上

	s, _ := json.Marshal(map[string]any{"mihomoMode": "manual", "mihomoAddr": addr, "suspectSec": 3, "stallSec": 6})
	if err := os.WriteFile(h.settings, s, 0o600); err != nil {
		t.Fatal(err)
	}
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	h.want("reclaude", model.StateIdle, model.LayerNone)
	h.mh.bump("c1", 4000, 0)
	h.tick()
	h.ticks(3)
	h.want("reclaude", model.StateSuspect, model.LayerLine) // 设置里的 3s
	h.ticks(3)
	h.want("reclaude", model.StateStall, model.LayerLine) // 6s

	// 改坏的配置：保留上一份，照实报
	os.WriteFile(cfgPath, []byte("targets: [ {name: x"), 0o600)
	future = future.Add(time.Hour)
	os.Chtimes(cfgPath, future, future)
	s2 := h.tick()
	if len(s2.Targets) != 2 || !strings.Contains(strings.Join(s2.SourceErr, "|"), "配置") {
		t.Fatalf("坏配置：targets=%d err=%v", len(s2.Targets), s2.SourceErr)
	}
}

// trainSlowNode 在 c1 上做 n 次「发请求 → wait 秒后回包 → 空闲 idle 秒」。
func (h *harness) trainNode(n, wait, idle int) {
	for i := 0; i < n; i++ {
		h.mh.bump("c1", 2000, 0)
		h.tick()
		h.ticks(wait - 1)
		h.mh.bump("c1", 0, 5000)
		h.tick()
		h.ticks(idle)
	}
}

func TestAdaptiveThresholdIsCapped(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "SLOW-1", 50001)
	h.tick()
	// 节点很慢：每次 12s 才回包 → p95 × 5 = 60s、× 10 = 120s，封顶成 30s / 90s
	h.trainNode(25, 12, 5)
	h.mh.bump("c1", 2000, 0)
	h.tick()
	h.ticks(29)
	h.want("reclaude", model.StateOK, model.LayerNone) // 29s
	h.tick()
	h.want("reclaude", model.StateSuspect, model.LayerLine) // 30s：上限
	h.ticks(59)
	h.want("reclaude", model.StateSuspect, model.LayerLine) // 89s
	h.tick()
	h.want("reclaude", model.StateStall, model.LayerLine) // 90s：上限
	d := h.e.Diagnose("reclaude")
	if !strings.Contains(d, "阈值：可疑 30s / 卡住 1m30s\n") {
		t.Fatalf("诊断里的阈值应当取整到秒：\n%s", d)
	}
}

func TestP95IgnoresIdleGapsAndHeartbeats(t *testing.T) {
	h := newHarness(t)
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.tick()
	// 每次请求 1s 就回包，之间空闲 25s、中间夹着保活心跳。空闲和心跳都不该把阈值抬上去
	for i := 0; i < 40; i++ {
		h.mh.bump("c1", 2000, 0)
		h.tick()
		h.mh.bump("c1", 0, 5000)
		h.tick()
		h.ticks(12)
		h.mh.bump("c1", 863, 658)
		h.tick()
		h.ticks(12)
	}
	h.mh.bump("c1", 2000, 0)
	h.tick()
	h.ticks(9)
	h.want("reclaude", model.StateOK, model.LayerNone) // 9s
	h.tick()
	h.want("reclaude", model.StateSuspect, model.LayerLine) // 10s：p95=1s，下限 10s
	h.ticks(20)
	h.want("reclaude", model.StateStall, model.LayerLine) // 30s
	if d := h.e.Diagnose("reclaude"); !strings.Contains(d, "阈值：可疑 10s / 卡住 30s\n") {
		t.Fatalf("诊断：\n%s", d)
	}
}

func TestMihomoAppsFilter(t *testing.T) {
	h := newHarness(t)
	procs := &mapProcs{apps: map[int]string{50001: "reclaude", 50002: "ego lite"}}
	h.e.opts.Procs = procs
	cfgPath := filepath.Join(h.dir, "targets.yaml")
	b, _ := os.ReadFile(cfgPath)
	cfg := strings.Replace(string(b), `hosts: ["*.reclaude.ai"] }`, `hosts: ["*.reclaude.ai"], apps: [ReClaude] }`, 1)
	if cfg == string(b) {
		t.Fatal("没改到配置")
	}
	os.WriteFile(cfgPath, []byte(cfg), 0o600)
	future := h.now.Add(time.Hour)
	os.Chtimes(cfgPath, future, future)

	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.mh.add("c2", "www.reclaude.ai", "JP-1", 50002) // 浏览器打开的网页
	h.tick()
	tg := h.want("reclaude", model.StateIdle, model.LayerNone)
	if len(tg.Conns) != 1 || tg.Conns[0].ID != "c1" || len(tg.Apps) != 1 || tg.Apps[0] != "reclaude" {
		t.Fatalf("只该算 reclaude 软件的连接：conns=%+v apps=%v", tg.Conns, tg.Apps)
	}
	// 浏览器发请求没回包：不关 ReClaude 的事
	h.mh.bump("c2", 4000, 0)
	h.ticks(40)
	h.want("reclaude", model.StateIdle, model.LayerNone)

	ds, err := h.e.Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if want := d.Host == "misaka.route.reclaude.ai"; d.Watched != want {
			t.Fatalf("%s 的已盯 = %v，要 %v", d.Host, d.Watched, want)
		}
	}

	// 发现面板带上软件加入：浏览器那一份单独成目标
	if err := h.e.AddTargetApps("ReClaude 网页", "*.reclaude.ai", []string{"ego lite"}); err != nil {
		t.Fatal(err)
	}
	h.tick()
	if web := h.target("reclaude-2"); len(web.Conns) != 1 || web.Conns[0].ID != "c2" {
		t.Fatalf("网页目标：%+v", web.Conns)
	}

	// 进程反查整轮失败：判不了软件，按 host 算（照实报错），不让目标整个变瞎
	procs.mu.Lock()
	procs.fail = true
	procs.mu.Unlock()
	s := h.tick()
	if tg := h.target("reclaude"); len(tg.Conns) != 2 {
		t.Fatalf("反查失败时按 host 算：%+v", tg.Conns)
	}
	if !strings.Contains(strings.Join(s.SourceErr, "|"), "进程归属") {
		t.Fatalf("反查失败要照实报：%v", s.SourceErr)
	}
}

// 只有一个日志数据源、而且文件不存在：没有可判的数据源，仍然照实报 UNKNOWN。
func TestLogOnlyTargetMissingFileIsUnknown(t *testing.T) {
	h := newHarness(t)
	cfgPath := filepath.Join(h.dir, "targets.yaml")
	b, _ := os.ReadFile(cfgPath)
	cfg := string(b) + fmt.Sprintf(`  - name: logonly
    sources:
      - { type: logtail, path: %q, progress: 'ok' }
`, filepath.Join(h.dir, "nope.log"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	future := h.now.Add(time.Hour)
	os.Chtimes(cfgPath, future, future)
	h.tick()
	tg := h.want("logonly", model.StateUnknown, model.LayerNone)
	if !strings.Contains(tg.Reason, "nope.log 不存在") {
		t.Fatalf("reason = %q", tg.Reason)
	}
	h.want("reclaude", model.StateIdle, model.LayerNone)
}

// 默认配置里 mirasim 的回合正则，喂 launch.log 里真实的事件顺序（只有 event= / ms= 字段）：
// launch → target-spawned → first-frame → turn-first-output → … → turn-result。
// 回合中线路在动、日志没新帧 → THINKING；回合结束后不再算在飞。
func TestMirasimDefaultTurnRegexesGiveThinking(t *testing.T) {
	c, err := config.Parse([]byte(config.DefaultYAML))
	if err != nil {
		t.Fatal(err)
	}
	var src config.Source
	for _, tc := range c.Targets {
		if tc.Name == "mirasim" {
			src = tc.Sources[1]
		}
	}
	if src.Start == "" || src.End == "" {
		t.Fatalf("默认 mirasim 没配回合正则：%+v", src)
	}
	h := newHarnessMirasimLog(t, fmt.Sprintf("progress: %q, start: %q, end: %q", src.Progress, src.Start, src.End))
	h.mh.add("m1", "api.mirasim.ai", "US-2", 50010)
	h.tick()

	// 一次性调用（launch 之后 once-exit，不出回合）：不能一直挂着在飞
	appendTo(t, h.launch, "event=launch\nevent=target-spawned ms=1\nevent=once-exit ms=60\nevent=route-resolved ms=0\n")
	h.ticks(70)
	h.want("mirasim", model.StateIdle, model.LayerNone)

	appendTo(t, h.launch, "event=launch\nevent=target-spawned ms=0\n")
	h.mh.bump("m1", 4000, 0)
	h.tick()
	h.want("mirasim", model.StateOK, model.LayerNone)
	h.ticks(2)
	appendTo(t, h.launch, "event=first-frame ms=2940\nevent=turn-first-output ms=2957\n")
	h.mh.bump("m1", 0, 300)
	h.tick()
	for i := 0; i < 40; i++ {
		h.mh.bump("m1", 0, 200)
		h.tick()
	}
	h.want("mirasim", model.StateThinking, model.LayerNone)
	appendTo(t, h.launch, "event=turn-result ms=485986\nevent=route-resolved ms=27\n")
	h.tick()
	h.want("mirasim", model.StateOK, model.LayerNone)
	h.ticks(61)
	h.want("mirasim", model.StateIdle, model.LayerNone)

	// 同一会话的下一个回合：没有 launch，从 turn-first-output 开始
	appendTo(t, h.launch, "event=route-resolved ms=0\nevent=turn-first-output ms=46\n")
	h.mh.bump("m1", 3000, 300)
	h.tick()
	for i := 0; i < 20; i++ {
		h.mh.bump("m1", 0, 200)
		h.tick()
	}
	h.want("mirasim", model.StateThinking, model.LayerNone)
	appendTo(t, h.launch, "event=turn-result ms=74917\n")
	h.tick()
	h.want("mirasim", model.StateOK, model.LayerNone)
}

// hookEventApp 写一行带发起软件的 hook 记录（新格式）。
func (h *harness) hookEventApp(ev, session, app string) {
	b, _ := json.Marshal(map[string]any{"ts": h.now, "event": ev, "tool": "Bash", "session": session, "app": app})
	appendTo(h.t, h.hook, string(b)+"\n")
}

// 两个目标都发了请求没回包。
func (h *harness) bothPending() {
	h.mh.add("c1", "misaka.route.reclaude.ai", "JP-1", 50001)
	h.mh.add("m1", "api.mirasim.ai", "US-2", 50010)
	h.tick()
	h.mh.bump("c1", 4000, 0)
	h.mh.bump("m1", 4000, 0)
	h.tick()
}

func TestBusyOnlyForCallerAppReclaudeToolDoesNotHideMirasimStall(t *testing.T) {
	h := newHarness(t)
	h.bothPending()
	h.hookEventApp("pre", "rc", "reclaude") // reclaude 会话在跑 build
	var sawSuspect, sawStall bool
	for i := 0; i < 40; i++ {
		h.tick()
		if tg := h.target("reclaude"); tg.State != model.StateBusy {
			t.Fatalf("第 %d 秒 reclaude 在跑工具，应当 busy，实际 %s", i, tg.State)
		}
		switch h.target("mirasim").State {
		case model.StateBusy:
			t.Fatalf("第 %d 秒 mirasim 被 reclaude 的工具压成了 busy", i)
		case model.StateSuspect:
			sawSuspect = true
		case model.StateStall:
			sawStall = true
		}
	}
	if !sawSuspect || !sawStall {
		t.Fatalf("mirasim 真卡住应当照报 suspect / stall：suspect=%v stall=%v，此刻 %s", sawSuspect, sawStall, h.target("mirasim").State)
	}
	if h.last.Alerting == 0 {
		t.Fatal("mirasim 卡住应当计入报警")
	}
}

func TestBusyOnlyForCallerAppMirasimToolOnlyMirasimBusy(t *testing.T) {
	h := newHarness(t)
	h.bothPending()
	h.hookEventApp("pre", "ms", "Mirasim") // 大小写和目标 name 不同也算
	h.ticks(15)
	h.want("mirasim", model.StateBusy, model.LayerNone)
	if st := h.target("reclaude").State; st != model.StateSuspect {
		t.Fatalf("mirasim 在跑工具时 reclaude 照常计时，15s 应当 suspect，实际 %s", st)
	}
	// 不相干的软件（比如 VS Code 里的 claude）在跑工具，谁都不压
	h.hookEventApp("post", "ms", "Mirasim")
	h.hookEventApp("pre", "vs", "Code")
	h.tick()
	if st := h.target("mirasim").State; st == model.StateBusy {
		t.Fatal("别的软件在跑工具不该压住 mirasim")
	}
	if st := h.target("reclaude").State; st == model.StateBusy {
		t.Fatal("别的软件在跑工具不该压住 reclaude")
	}
	// 旧格式（没有 app）照旧对所有目标生效
	h.hookEvent("pre")
	h.tick()
	h.want("reclaude", model.StateBusy, model.LayerNone)
	h.want("mirasim", model.StateBusy, model.LayerNone)
}

func TestBusyAppsFromMihomoAppsOrName(t *testing.T) {
	withApps := newTargetState(config.Target{Name: "cloud", Sources: []config.Source{
		{Type: config.TypeMihomo, Hosts: []string{"*.example.com"}, Apps: []string{"Mirasim"}},
		{Type: config.TypeMihomo, Hosts: []string{"*.example.org"}, Apps: []string{"reclaude"}},
	}})
	if got := withApps.busyApps(); len(got) != 2 || got[0] != "Mirasim" || got[1] != "reclaude" {
		t.Fatalf("配了 apps 时用 apps：%v", got)
	}
	noApps := newTargetState(config.Target{Name: "mirasim", Sources: []config.Source{{Type: config.TypeMihomo, Hosts: []string{"*.example.com"}}}})
	if got := noApps.busyApps(); len(got) != 1 || got[0] != "mirasim" {
		t.Fatalf("没配 apps 时用目标 name：%v", got)
	}
}
