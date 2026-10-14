package engine

import (
	"errors"
	"fmt"
	"io/fs"
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

// pendReq 是一条连接上发出、还没等到回包的请求。
type pendReq struct {
	since  time.Time // 发请求的时刻（BUSY / UNKNOWN / 唤醒宽限期间往后挪）
	goneAt time.Time // 这条连接从 /connections 里消失的时刻；零值 = 还在
}

type openStall struct {
	at     time.Time // 进 STALL 的时刻
	since  time.Time // 从什么时候没进展
	layer  model.Layer
	node   string
	reason string
}

// lineSel 是一个 mihomo 数据源挑连接的条件：host 通配，加可选的软件名。
type lineSel struct {
	hosts []string
	apps  []string
}

type targetState struct {
	cfg      config.Target
	lines    []lineSel
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

	lastActivity time.Time
	pending      map[string]*pendReq // 连接 ID → 那条连接上还没回包的请求；只有这条连接自己的下行算回包
	downWin      []model.Sample      // 最近的真下行（去掉 ping-pong 小包），判「线路在动」用
	turnOpen     bool
	turnSince    time.Time
	// hook 层（#21 M2）：hook 说本目标的软件有会话在等模型回包。hookStart 是 hook 给的在飞起点；
	// 起点之后目标连接上真发出过模型请求（一秒上行 ≥ HookRequestMin）才「上膛」，hookSince 从上膛时刻起、
	// 每次真下行往后挪；零值 = 不在飞或还没上膛（这个软件的模型流量不走本目标的线路时永远不上膛，不误报）。
	hookStart time.Time
	hookSince time.Time
	hookReqAt time.Time // 最近一次上膛的模型请求时刻

	errEvents  []logEvent
	acctEvents []logEvent

	intervals map[string][]float64 // 节点 → 最近的进展间隔（秒），只收在飞期间的
	ivFrom    time.Time            // 下一个进展间隔从哪儿量起：发请求 / 回合开始 / 上一次在飞的进展；零值 = 没在飞

	trace []model.Sample
	stall *openStall

	lastSuspect, lastStall time.Duration
}

func newTargetState(tc config.Target) *targetState {
	t := &targetState{cfg: tc, state: model.StateIdle, intervals: map[string][]float64{}, pending: map[string]*pendReq{}}
	for _, s := range tc.Sources {
		switch s.Type {
		case config.TypeMihomo:
			t.hasLine = true
			t.lines = append(t.lines, lineSel{hosts: s.Hosts, apps: s.Apps})
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

// matchesHost 只看 host：决定要不要给这条连接反查进程（软件名要反查之后才知道）。
func (t *targetState) matchesHost(host string) bool {
	for _, l := range t.lines {
		if source.MatchAny(l.hosts, host) {
			return true
		}
	}
	return false
}

// matchesConn 看 host 和软件：数据源写了 apps 时，只算这些软件发的连接。
// appKnown 为 false（这一轮进程反查整体失败或平台不支持）时判不了软件，按 host 算，免得目标整个变瞎；
// 反查成功、只是这条连接没查到（多半是已关的短连接）时，限了软件的数据源不算它。
func (t *targetState) matchesConn(host, app string, appKnown bool) bool {
	for _, l := range t.lines {
		if !source.MatchAny(l.hosts, host) {
			continue
		}
		if len(l.apps) == 0 || !appKnown || source.MatchApp(l.apps, app) {
			return true
		}
	}
	return false
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
	procsOK        bool // 这一轮进程反查成功（软件名可信）
	connMeta       map[string]*connState
	suspect, stall time.Duration
	// gap：这一轮和上一轮之间断了档（睡眠唤醒，墙钟或单调钟跳了 > GapAfter）；before 是断档前最后一轮的时刻。
	gap    bool
	before time.Time
	grace  bool      // 唤醒后的宽限期：在飞也不升级成可疑 / 卡住，计时暂停
	noHook bool      // 没检测到 hook：回合层最多报到 SUSPECT
	rounds bool      // hook 带回合事件：「在等模型」归 hook 层判，回合层的线路结论也只到 SUSPECT
	flight time.Time // hook 说本目标的软件最早的在飞起点；零值 = 没有在等回包的会话
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

	// 日志：每轮都读，保持偏移。文件不存在（被盯的程序没装、还没跑过）只记一条数据源问题，
	// 这一路不算，目标照其余数据源判；其余数据源也都读不到时才 UNKNOWN。别的读错照旧 UNKNOWN。
	t.logErrs = t.logErrs[:0]
	var logProgress, starts, ends int
	usable := 0 // 这一轮读得到的数据源个数
	if t.hasLine && in.mihomoErr == nil {
		usable++
	}
	var missing []string
	turnLogMissing := false
	for i, lt := range t.logs {
		r, err := lt.Poll()
		switch {
		case err == nil:
			usable++
		case errors.Is(err, fs.ErrNotExist):
			msg := fmt.Sprintf("日志 %s 不存在：%v（这一路不算，按其余数据源判）", t.logNames[i], err)
			missing = append(missing, msg)
			t.logErrs = append(t.logErrs, msg)
			turnLogMissing = turnLogMissing || lt.HasTurns()
		default:
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
	if usable == 0 && len(unread) == 0 {
		unread = append(unread, missing...) // 一路都读不到：没有可判的数据源
	}

	var lerr error
	// 睡眠唤醒（断档）：睡前在等的请求不再等，间隔起点作废，回合从现在重新计时；
	// 开着的卡住按「未恢复」收尾，时长截到睡前，账本不含睡眠。之后的宽限期由 in.grace 管。
	if in.gap {
		t.pending = map[string]*pendReq{}
		t.ivFrom = time.Time{}
		t.downWin = nil
		if t.turnOpen {
			t.turnSince = now
		}
		if !t.hookSince.IsZero() {
			t.hookSince = now
		}
		if t.stall != nil {
			lerr = t.closeStall(led, in.before, false)
		}
	}

	// 连接：挑出本目标的，算增量；等回包按连接记
	waiting := len(t.pending) > 0 || t.turnOpen
	var dUp, dDown, realDown int64
	newReq, replied := false, false
	bigUp := false // 这一轮有连接一秒上行 ≥ HookRequestMin（hook 层上膛用）
	var conns []model.Conn
	nodeDown, nodeTotal := map[string]int64{}, map[string]int64{}
	appSet := map[string]bool{}
	present := make(map[string]bool, len(in.conns))
	for _, c := range in.conns {
		present[c.ID] = true
		host := c.HostName()
		if !t.matchesHost(host) {
			continue
		}
		var pr procattr.Proc
		if p, err := strconv.Atoi(c.Metadata.SourcePort); err == nil {
			pr = in.procs[p]
		}
		if !t.matchesConn(host, pr.App, in.procsOK) {
			continue
		}
		d := in.deltas[c.ID]
		dUp += d.up
		dDown += d.down
		// 同一秒上下行都是小包：保活 ping-pong，既不是请求也不是回包
		pingPong := d.up > 0 && d.down > 0 && d.up < RequestMinBytes && d.down < RequestMinBytes
		if !pingPong {
			realDown += d.down
		}
		bigUp = bigUp || d.up >= HookRequestMin
		// 这条连接上行够大：发了请求，开始等这条连接的回包（已经在等的不重置起点）
		if d.up >= RequestMinBytes {
			newReq = true
			if t.pending[c.ID] == nil {
				t.pending[c.ID] = &pendReq{since: now}
			}
		}
		// 只有发请求那条连接自己的下行算回包；别的连接的心跳不算
		if p := t.pending[c.ID]; p != nil && d.down > 0 && !pingPong {
			delete(t.pending, c.ID)
			replied = true
		}
		mc := model.Conn{ID: c.ID, Host: host, Node: c.Node(), Up: c.Upload, Down: c.Download, Started: c.Start, PID: pr.PID, App: pr.App}
		if st := in.connMeta[c.ID]; st != nil && !st.lastChange.IsZero() {
			mc.IdleFor = math.Max(0, now.Sub(st.lastChange).Seconds())
		}
		if pr.App != "" {
			appSet[pr.App] = true
		}
		nodeDown[mc.Node] += d.down
		nodeTotal[mc.Node] += c.Download
		conns = append(conns, mc)
	}
	// 在等的连接没了：客户端换连接重发了（来了新请求）就不再等旧的；
	// 没有新请求、过了 IdleAfter 也没回来 → 这次请求放弃了，不再算在飞（开着的卡住按未恢复收尾）。
	// mihomo 读不到的这一轮看不见连接，不算「没了」。
	if in.mihomoErr == nil {
		for id, p := range t.pending {
			switch {
			case present[id]:
				p.goneAt = time.Time{}
			case newReq:
				delete(t.pending, id)
			case p.goneAt.IsZero():
				p.goneAt = now
			case now.Sub(p.goneAt) >= IdleAfter:
				delete(t.pending, id)
			}
		}
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
	if realDown > 0 {
		t.downWin = append(t.downWin, model.Sample{At: now, Down: realDown})
	}
	for len(t.downWin) > 0 && now.Sub(t.downWin[0].At) > downWinKeep {
		t.downWin = t.downWin[1:]
	}

	// 事件
	if dUp >= RequestMinBytes || dDown >= RequestMinBytes || (realDown > 0 && waiting) || logProgress > 0 || starts > 0 || ends > 0 {
		t.lastActivity = now
	}
	// 进展间隔（自适应阈值用）只量「在飞」的那一段：从发请求 / 回合开始，到每一次真进展。
	// 空闲（上一次回复结束到下一次发请求）和保活心跳（小包、没在等回包）都不进来。
	if dUp >= RequestMinBytes || starts > 0 {
		t.ivFrom = now
	}
	realProgress := logProgress > 0 || realDown >= RequestMinBytes || (realDown > 0 && waiting)
	if realProgress && !t.ivFrom.IsZero() {
		if gap := now.Sub(t.ivFrom); gap > 0 && gap <= t.lastStall {
			iv := append(t.intervals[node], gap.Seconds())
			if len(iv) > maxIntervals {
				iv = iv[len(iv)-maxIntervals:]
			}
			t.intervals[node] = iv
		}
		t.ivFrom = now
	}
	// 「真进展」：卡住只因为它才算恢复。在等的连接回了包、日志新帧、回合结束，
	// 或者没配 progress 的回合里来了真下行。
	// hook 层：在飞期间目标连接的任何真下行（去掉 ping-pong）、日志进展都算进展
	if in.flight.IsZero() {
		t.hookStart, t.hookSince = time.Time{}, time.Time{}
	} else {
		if t.hookStart.IsZero() || in.flight.After(t.hookStart) {
			t.hookStart = in.flight
			if t.hookReqAt.Before(in.flight) {
				t.hookSince = time.Time{} // 新的一段在飞（工具结束、换了个会话）：等这段的模型请求再上膛
			}
		}
		if bigUp && !now.Before(t.hookStart) {
			t.hookSince, t.hookReqAt = now, now
		}
		if !t.hookSince.IsZero() && (realDown > 0 || logProgress > 0) {
			t.hookSince = now
		}
	}
	hookFlight := !t.hookSince.IsZero()
	progressed := replied || logProgress > 0 || ends > 0 || (t.turnOpen && !t.progLog && realDown > 0) || (hookFlight && realDown > 0)
	if logProgress > 0 {
		clear(t.pending) // 日志说有进展：字节层不再等
	}
	// 回合层：日志回合开始到结束之间，进展只认日志新帧（配了 progress 时），字节只算「线路在动」
	if starts > 0 {
		t.turnOpen, t.turnSince = true, now
	}
	if t.turnOpen && (logProgress > 0 || (!t.progLog && realDown > 0)) {
		t.turnSince = now
	}
	if ends > 0 || turnLogMissing {
		t.turnOpen = false // 给回合的日志不见了：看不到回合结束，不拿它判卡住
	}

	suspect, stall := t.thresholds(in, node)
	t.lastSuspect, t.lastStall = suspect, stall

	pendingSince := t.pendingSince()
	inFlight := !pendingSince.IsZero() || t.turnOpen || hookFlight
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
	case in.grace && inFlight:
		// 刚从睡眠唤醒：网络要一会儿才回来，宽限期内不升级，计时暂停
		t.state = model.StateOK
		reason = fmt.Sprintf("刚从睡眠唤醒，%.0fs 内不判卡住", WakeGrace.Seconds())
		t.pause(in.dt)
	case inFlight:
		judged, why := t.judge(now, stall)
		best := verdict{state: model.StateOK}
		if in.rounds && !in.flight.IsZero() {
			// hook 说这个目标有会话在等模型：「卡没卡」只由下面的 hook 层判，字节层不判。本机重放 53 分钟真实流量：
			// reclaude 网关一条连接常常只往上传（上 MB）、回复走别的连接，按连接等回包会挂着几分钟的「慢 / 卡住」。
			// hook 不知道有会话在飞时（装回合事件之前起的会话、不是 Claude 的流量）字节层照常判——09:22 那次真断网
			// 就是这样报出来的。
			pendingSince = time.Time{}
		}
		if !pendingSince.IsZero() {
			// 请求发出后一个字节都没回：线路在不在动都不算 THINKING
			best = t.classify(now, pendingSince, false, suspect, stall, judged, why, node)
		}
		if t.turnOpen && !in.rounds {
			// 线路在动要真下行：最近一个 suspect 窗口里去掉 ping-pong 的下行累计够 RequestMinBytes，心跳撑不起来
			lineMoving := t.hasLine && t.progLog && t.recentDown(now, suspect) >= RequestMinBytes
			v := t.classify(now, t.turnSince, lineMoving, suspect, stall, judged, why, node)
			if in.noHook && v.state == model.StateStall && (v.layer == model.LayerLine || v.layer == model.LayerNone) {
				// 没接 hook：回合日志只有首帧和结束两种进展，回合里跑几分钟本地工具很常见，分不清在跑工具还是卡住，
				// 只报慢、不报卡住。日志里有 429 / 5xx、配额线索（B / C）时不是这回事，照常报卡住。
				v.state = model.StateSuspect
				v.reason = "没装 hook，回合内只报慢：" + v.reason
			}
			if v.rank() > best.rank() {
				best = v
			}
		}
		if hookFlight {
			// hook 说在等模型回包：从在飞起点 / 最近一次真下行起没有字节就是卡住（回包到一半断流也算）
			// 阈值另有下限：本机影子实测在飞期间（模型在想）最长 36s 一个字节都没有，按 30s 判会误报
			hs, hst := max(suspect, HookMinSuspect), max(stall, HookMinStall)
			if v := t.classify(now, t.hookSince, false, hs, hst, judged, why, node); v.rank() > best.rank() {
				best = v
			}
		}
		if t.stall != nil && best.state != model.StateStall && !progressed {
			// 进了 STALL 只有真进展才出来：线路又动了（THINKING）、重发了请求、阈值变了都还是同一次卡住
			best = verdict{state: model.StateStall, stuck: now.Sub(t.stall.since), since: t.stall.since}
		}
		t.state, layer, reason, stuck, stuckSince = best.state, best.layer, best.reason, best.stuck, best.since
		if t.state == model.StateStall && t.stall != nil {
			// 层在进 STALL 那一刻判定，之后不随窗口滑动改口；A 层的秒数跟着走
			layer, reason = t.stall.layer, t.stall.reason
			if layer == model.LayerLine {
				_, reason = t.lineLayer(stuck, node)
			}
		}
	case active:
		t.state = model.StateOK
	default:
		t.state = model.StateIdle
	}
	t.layer = layer
	// 不在飞了（空闲、读不到、本地忙），或者等了超过卡住阈值（那是卡顿、不是常态节奏）：下一段间隔重新量
	if t.state == model.StateIdle || t.state == model.StateUnknown || t.state == model.StateBusy ||
		(!t.ivFrom.IsZero() && now.Sub(t.ivFrom) > stall) {
		t.ivFrom = time.Time{}
	}

	// 账本：进 STALL 开一条；有真进展回到 OK 记「已恢复」，转 idle / unknown、或者没有真进展就不在飞了
	// （等的连接没了）记未恢复；BUSY、SUSPECT 期间保持（不是进展），免得来回跳一次记一行。
	recovered := false
	setErr := func(err error) {
		if err != nil {
			lerr = err
		}
	}
	switch {
	case t.state == model.StateStall && t.stall == nil:
		t.stall = &openStall{at: now, since: stuckSince, layer: layer, node: node, reason: reason}
	case t.stall != nil && t.state == model.StateOK:
		recovered = progressed
		setErr(t.closeStall(led, now, recovered))
	case t.stall != nil && (t.state == model.StateIdle || t.state == model.StateUnknown):
		setErr(t.closeStall(led, now, false))
	}

	mt := model.Target{
		Name: t.cfg.Name, Label: t.cfg.DisplayLabel(), State: t.state, Layer: layer, Reason: reason,
		Node: node, Apps: sortedKeys(appSet), Conns: conns, LogPath: t.logPath,
		Trace: append([]model.Sample(nil), t.trace...), Recovered: recovered,
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

// pause 把计时起点往后挪 d：BUSY、UNKNOWN、唤醒宽限期间不计入「没进展」。
func (t *targetState) pause(d time.Duration) {
	for _, p := range t.pending {
		p.since = p.since.Add(d)
	}
	if t.turnOpen {
		t.turnSince = t.turnSince.Add(d)
	}
	if !t.hookSince.IsZero() {
		t.hookSince = t.hookSince.Add(d)
	}
}

// pendingSince 是还在等的请求里最早的那个的起点；零值 = 没在等。
func (t *targetState) pendingSince() time.Time {
	var oldest time.Time
	for _, p := range t.pending {
		if oldest.IsZero() || p.since.Before(oldest) {
			oldest = p.since
		}
	}
	return oldest
}

// recentDown 是最近 window 内的真下行字节数（去掉 ping-pong 小包）。
func (t *targetState) recentDown(now time.Time, window time.Duration) int64 {
	var n int64
	for _, s := range t.downWin {
		if now.Sub(s.At) < window {
			n += s.Down
		}
	}
	return n
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

// thresholds：suspect = clamp(p95 × 5, 配置值, 30s)，stall = clamp(p95 × 10, 配置值, 90s)；
// p95 是该节点在飞期间进展间隔的 95 分位，攒不够 minIntervals 个时直接用配置值。
// 配置值：目标自己写的 > 界面设置 > targets.yaml 的 defaults（默认 10s / 30s）；配置值比上限还大时以配置值为准。
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
		sus = clampDur(time.Duration(p*5*float64(time.Second)), sus, max(sus, MaxSuspect))
		st = clampDur(time.Duration(p*10*float64(time.Second)), st, max(st, MaxStall))
	}
	if st <= sus {
		st = sus * 3
	}
	return sus, st
}

func clampDur(v, lo, hi time.Duration) time.Duration {
	return min(max(v, lo), hi)
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
