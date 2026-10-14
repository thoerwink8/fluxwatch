package engine

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/config"
	"github.com/thoerwink8/fluxwatch/internal/ledger"
	"github.com/thoerwink8/fluxwatch/internal/mihomo"
	"github.com/thoerwink8/fluxwatch/internal/model"
	"github.com/thoerwink8/fluxwatch/internal/procattr"
	"github.com/thoerwink8/fluxwatch/internal/source"
)

type logEvent struct {
	at   time.Time
	file string
	code string // 状态码；配额线索为空
}

type openStall struct {
	at     time.Time // 进 STALL 的时刻
	since  time.Time // 从什么时候没进展
	layer  model.Layer
	node   string
	reason string
}

type targetState struct {
	cfg      config.Target
	hosts    []string
	logs     []*source.Logtail
	logInit  []error // 建 logtail 时的错（正则、路径展开）
	logErrs  []string
	logPath  string
	progLog  bool // 有带 progress 正则的日志：进展只认日志
	turnLog  bool
	hasLine  bool // 有 mihomo 数据源
	logNames []string

	state model.State
	layer model.Layer

	lastProgress time.Time
	lastDown     time.Time
	lastActivity time.Time
	pendingSince time.Time // 发了请求、还没等到进展；零值 = 没在等
	turnOpen     bool
	turnSince    time.Time

	errEvents  []logEvent
	acctEvents []logEvent

	intervals map[string][]float64 // 节点 → 最近的进展间隔（秒）

	trace []model.Sample
	stall *openStall

	lastSuspect, lastStall time.Duration
}

func newTargetState(tc config.Target) *targetState {
	t := &targetState{cfg: tc, state: model.StateIdle, intervals: map[string][]float64{}}
	for _, s := range tc.Sources {
		switch s.Type {
		case config.TypeMihomo:
			t.hasLine = true
			t.hosts = append(t.hosts, s.Hosts...)
		case config.TypeLogtail:
			lt, err := source.NewLogtail(s)
			if err != nil {
				t.logInit = append(t.logInit, fmt.Errorf("日志 %s：%w", s.Path, err))
				continue
			}
			if t.logPath == "" {
				t.logPath = lt.Path
			}
			t.progLog = t.progLog || lt.HasProgress()
			t.turnLog = t.turnLog || lt.HasTurns()
			t.logs = append(t.logs, lt)
			t.logNames = append(t.logNames, filepath.Base(lt.Path))
		}
	}
	return t
}

func (t *targetState) matches(host string) bool {
	return len(t.hosts) > 0 && source.MatchAny(t.hosts, host)
}

func (t *targetState) closeLogs() {
	for _, l := range t.logs {
		l.Close()
	}
}

type tickInput struct {
	now            time.Time
	dt             time.Duration
	busy           bool
	mihomoErr      error
	conns          []mihomo.Connection
	deltas         map[string]delta
	procs          map[int]procattr.Proc
	connMeta       map[string]*connState
	suspect, stall time.Duration
}

// tick 推进一轮状态机，返回这一轮的 model.Target。账本写失败时返回错误（判定照常）。
func (t *targetState) tick(in tickInput, led *ledger.Ledger) (model.Target, error) {
	now := in.now
	var unread []string
	for _, err := range t.logInit {
		unread = append(unread, err.Error())
	}
	if t.hasLine && in.mihomoErr != nil {
		unread = append(unread, "mihomo 读不到")
	}

	// 日志：每轮都读，保持偏移
	t.logErrs = t.logErrs[:0]
	var logProgress, starts, ends int
	for i, lt := range t.logs {
		r, err := lt.Poll()
		if err != nil {
			msg := fmt.Sprintf("日志 %s 读不到：%v", t.logNames[i], err)
			unread = append(unread, msg)
			t.logErrs = append(t.logErrs, msg)
		}
		logProgress += r.Progress
		starts += r.Starts
		ends += r.Ends
		for _, c := range r.Errors {
			t.errEvents = append(t.errEvents, logEvent{at: now, file: t.logNames[i], code: c})
		}
		for j := 0; j < r.Account; j++ {
			t.acctEvents = append(t.acctEvents, logEvent{at: now, file: t.logNames[i]})
		}
	}
	t.pruneEvents(now)

	// 连接：挑出本目标的，算增量
	var dUp, dDown int64
	var conns []model.Conn
	nodeDown, nodeTotal := map[string]int64{}, map[string]int64{}
	appSet := map[string]bool{}
	for _, c := range in.conns {
		host := c.HostName()
		if !t.matches(host) {
			continue
		}
		d := in.deltas[c.ID]
		dUp += d.up
		dDown += d.down
		mc := model.Conn{ID: c.ID, Host: host, Node: c.Node(), Up: c.Upload, Down: c.Download, Started: c.Start}
		if st := in.connMeta[c.ID]; st != nil && !st.lastChange.IsZero() {
			mc.IdleFor = math.Max(0, now.Sub(st.lastChange).Seconds())
		}
		if p, err := strconv.Atoi(c.Metadata.SourcePort); err == nil {
			if pr, ok := in.procs[p]; ok {
				mc.PID, mc.App = pr.PID, pr.App
				if pr.App != "" {
					appSet[pr.App] = true
				}
			}
		}
		nodeDown[mc.Node] += d.down
		nodeTotal[mc.Node] += c.Download
		conns = append(conns, mc)
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].Started.Before(conns[j].Started) })
	// 主要节点：这一轮下行最多的；都没动时按累计下行
	node := ""
	for n := range nodeDown {
		if node == "" || nodeDown[n] > nodeDown[node] ||
			(nodeDown[n] == nodeDown[node] && (nodeTotal[n] > nodeTotal[node] || (nodeTotal[n] == nodeTotal[node] && n < node))) {
			node = n
		}
	}

	t.trace = append(t.trace, model.Sample{At: now, Down: dDown, Up: dUp})
	if len(t.trace) > TraceLen {
		t.trace = append([]model.Sample(nil), t.trace[len(t.trace)-TraceLen:]...)
	}

	// 事件
	if dDown > 0 {
		t.lastDown = now
	}
	waiting := !t.pendingSince.IsZero() || t.turnOpen
	if dUp >= RequestMinBytes || dDown >= RequestMinBytes || (dDown > 0 && waiting) || logProgress > 0 || starts > 0 || ends > 0 {
		t.lastActivity = now
	}
	// 字节层：发了请求（上行够大）就开始等，任何下行或日志进展都算回包
	if dUp >= RequestMinBytes && t.pendingSince.IsZero() {
		t.pendingSince = now
	}
	anyProgress := dDown > 0 || logProgress > 0
	if anyProgress {
		t.pendingSince = time.Time{}
		if !t.lastProgress.IsZero() && t.state != model.StateIdle && t.state != model.StateUnknown {
			gap := now.Sub(t.lastProgress).Seconds()
			if gap > 0 && gap <= 600 {
				iv := append(t.intervals[node], gap)
				if len(iv) > maxIntervals {
					iv = iv[len(iv)-maxIntervals:]
				}
				t.intervals[node] = iv
			}
		}
		t.lastProgress = now
	}
	// 回合层：日志回合开始到结束之间，进展只认日志新帧（配了 progress 时），字节只算「线路在动」
	if starts > 0 {
		t.turnOpen, t.turnSince = true, now
	}
	if t.turnOpen && (logProgress > 0 || (!t.progLog && dDown > 0)) {
		t.turnSince = now
	}
	if ends > 0 {
		t.turnOpen = false
	}

	suspect, stall := t.thresholds(in, node)
	t.lastSuspect, t.lastStall = suspect, stall

	inFlight := !t.pendingSince.IsZero() || t.turnOpen
	active := inFlight || (!t.lastActivity.IsZero() && now.Sub(t.lastActivity) < IdleAfter)
	reason := ""
	layer := model.LayerNone
	var stuck time.Duration
	stuckSince := now

	switch {
	case len(unread) > 0:
		// 读不到：照实报 unknown；计时暂停，读回来之后不因为看不见的这段直接判卡住
		t.state = model.StateUnknown
		reason = strings.Join(unread, "；")
		t.pause(in.dt)
	case in.busy && active:
		// 本地工具在跑：计时暂停（把起点往后挪 dt），工具结束接着算
		t.state = model.StateBusy
		reason = "本地工具在跑，网络静默属正常"
		t.pause(in.dt)
	case inFlight:
		judged, why := t.judge(now, stall)
		best := verdict{state: model.StateOK}
		if !t.pendingSince.IsZero() {
			// 请求发出后一个字节都没回：线路在不在动都不算 THINKING
			best = t.classify(now, t.pendingSince, false, suspect, stall, judged, why, node)
		}
		if t.turnOpen {
			lineMoving := t.hasLine && t.progLog && !t.lastDown.IsZero() && now.Sub(t.lastDown) < suspect
			if v := t.classify(now, t.turnSince, lineMoving, suspect, stall, judged, why, node); v.rank() > best.rank() {
				best = v
			}
		}
		t.state, layer, reason, stuck, stuckSince = best.state, best.layer, best.reason, best.stuck, best.since
		if t.state == model.StateStall && t.stall != nil && t.stall.layer != model.LayerLine {
			// 层在进 STALL 那一刻判定，之后不随窗口滑动改口
			layer, reason = t.stall.layer, t.stall.reason
		}
	case active:
		t.state = model.StateOK
	default:
		t.state = model.StateIdle
	}
	t.layer = layer

	// 账本：进 STALL 开一条；有进展回到 OK 记「已恢复」，转 idle / unknown 记未恢复；
	// BUSY、THINKING、SUSPECT 期间保持（不是进展），免得来回跳一次记一行。
	var lerr error
	switch {
	case t.state == model.StateStall && t.stall == nil:
		t.stall = &openStall{at: now, since: stuckSince, layer: layer, node: node, reason: reason}
	case t.stall != nil && t.state == model.StateOK:
		lerr = t.closeStall(led, now, true)
	case t.stall != nil && (t.state == model.StateIdle || t.state == model.StateUnknown):
		lerr = t.closeStall(led, now, false)
	}

	mt := model.Target{
		Name: t.cfg.Name, Label: t.cfg.DisplayLabel(), State: t.state, Layer: layer, Reason: reason,
		Node: node, Apps: sortedKeys(appSet), Conns: conns, LogPath: t.logPath,
		Trace: append([]model.Sample(nil), t.trace...),
	}
	if mt.Conns == nil {
		mt.Conns = []model.Conn{}
	}
	if t.state == model.StateSuspect || t.state == model.StateStall {
		mt.StuckFor = stuck.Seconds()
	}
	if led != nil {
		mt.TodayCount = led.TodayCount(now, t.cfg.Name, node)
	}
	if t.stall != nil && (node == "" || t.stall.node == node) {
		mt.TodayCount++
	}
	return mt, lerr
}

// pause 把计时起点往后挪 d：BUSY、UNKNOWN 期间不计入「没进展」。
func (t *targetState) pause(d time.Duration) {
	if !t.pendingSince.IsZero() {
		t.pendingSince = t.pendingSince.Add(d)
	}
	if t.turnOpen {
		t.turnSince = t.turnSince.Add(d)
	}
}

type verdict struct {
	state  model.State
	layer  model.Layer
	reason string
	stuck  time.Duration
	since  time.Time
}

func (v verdict) rank() int {
	switch v.state {
	case model.StateStall:
		return 4
	case model.StateSuspect:
		return 3
	case model.StateThinking:
		return 2
	}
	return 1
}

// classify 按「从 since 起没进展」判 OK / THINKING / SUSPECT / STALL，并给层。
func (t *targetState) classify(now, since time.Time, lineMoving bool, suspect, stall time.Duration, judged model.Layer, why, node string) verdict {
	stuck := now.Sub(since)
	v := verdict{state: model.StateOK, stuck: stuck, since: since}
	switch {
	case stuck < suspect:
	case judged != model.LayerNone && (stuck >= stall || !lineMoving):
		v.state, v.layer, v.reason = model.StateSuspect, judged, why
	case lineMoving:
		v.state = model.StateThinking
		v.reason = fmt.Sprintf("线路在动、日志干净，%.0fs 没有新帧", stuck.Seconds())
	default:
		v.state = model.StateSuspect
		v.layer, v.reason = t.lineLayer(stuck, node)
	}
	if v.state == model.StateSuspect && stuck >= stall {
		v.state = model.StateStall
	}
	return v
}

// thresholds：suspect = max(配置值, 该节点进展间隔 p95 × 5)，stall = max(配置值, p95 × 10)。
// 配置值：目标自己写的 > 界面设置 > targets.yaml 的 defaults。
func (t *targetState) thresholds(in tickInput, node string) (time.Duration, time.Duration) {
	sus, st := in.suspect, in.stall
	if t.cfg.Suspect > 0 {
		sus = t.cfg.Suspect.Std()
	}
	if t.cfg.Stall > 0 {
		st = t.cfg.Stall.Std()
	}
	if iv := t.intervals[node]; len(iv) >= minIntervals {
		p := p95(iv)
		if a := time.Duration(p * 5 * float64(time.Second)); a > sus {
			sus = a
		}
		if a := time.Duration(p * 10 * float64(time.Second)); a > st {
			st = a
		}
	}
	if st <= sus {
		st = sus * 3
	}
	return sus, st
}

func p95(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Ceil(0.95*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	return s[i]
}

// judge 按顺序判 B（日志 429 / 5xx）、C（换号、配额线索）；都没有返回 LayerNone。窗口是最近一个 stall 阈值。
func (t *targetState) judge(now time.Time, window time.Duration) (model.Layer, string) {
	byFile := map[string]map[string]int{}
	for _, ev := range t.errEvents {
		if now.Sub(ev.at) > window {
			continue
		}
		if byFile[ev.file] == nil {
			byFile[ev.file] = map[string]int{}
		}
		byFile[ev.file][ev.code]++
	}
	if len(byFile) > 0 {
		var parts []string
		for _, f := range sortedKeysM(byFile) {
			parts = append(parts, f+" "+formatCodes(byFile[f]))
		}
		return model.LayerUpstream, strings.Join(parts, "；")
	}
	acct := map[string]int{}
	for _, ev := range t.acctEvents {
		if now.Sub(ev.at) <= window {
			acct[ev.file]++
		}
	}
	if len(acct) > 0 {
		var parts []string
		for _, f := range sortedKeysM(acct) {
			parts = append(parts, fmt.Sprintf("%s 换号/配额线索 ×%d", f, acct[f]))
		}
		return model.LayerAccount, strings.Join(parts, "；")
	}
	return model.LayerNone, ""
}

// lineLayer：日志干净、线路字节也停了 → A。没配 mihomo 的目标判不了线路，不给层。
func (t *targetState) lineLayer(stuck time.Duration, node string) (model.Layer, string) {
	if !t.hasLine {
		return model.LayerNone, fmt.Sprintf("日志 %.0fs 没有新进展（没配 mihomo，判不了线路）", stuck.Seconds())
	}
	if node == "" {
		return model.LayerLine, fmt.Sprintf("请求发出 %.0fs 无回包，连接已不在", stuck.Seconds())
	}
	return model.LayerLine, fmt.Sprintf("节点 %s 字节停了 %.0fs", node, stuck.Seconds())
}

func (t *targetState) pruneEvents(now time.Time) {
	const keep = time.Hour
	cut := func(evs []logEvent) []logEvent {
		i := 0
		for i < len(evs) && now.Sub(evs[i].at) > keep {
			i++
		}
		return evs[i:]
	}
	t.errEvents = cut(t.errEvents)
	t.acctEvents = cut(t.acctEvents)
}

func (t *targetState) recentCounts(now time.Time, window time.Duration) (map[string]int, int) {
	m := map[string]int{}
	for _, ev := range t.errEvents {
		if now.Sub(ev.at) <= window {
			m[ev.code]++
		}
	}
	n := 0
	for _, ev := range t.acctEvents {
		if now.Sub(ev.at) <= window {
			n++
		}
	}
	return m, n
}

// closeStall 把开着的卡住写进账本。
func (t *targetState) closeStall(led *ledger.Ledger, now time.Time, recovered bool) error {
	if t.stall == nil {
		return nil
	}
	s := t.stall
	t.stall = nil
	if led == nil {
		return nil
	}
	e := ledger.Entry{At: s.at, Target: t.cfg.Name, Layer: string(s.layer), Node: s.node, Seconds: now.Sub(s.since).Seconds(), Reason: s.reason}
	if recovered {
		r := now
		e.RecoveredAt = &r
	}
	return led.Append(e)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysM[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
