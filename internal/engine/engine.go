// Package engine 每秒读一轮数据源，按目标跑状态机，产出 model.Snapshot；实现 model.Provider。
//
// 判定照 docs/design.md 第四节。几个实现上的取舍写在这里：
//   - 字节层：目标连接一秒内上行 ≥ RequestMinBytes（滤掉 h2 PING 一类保活小包）算发了请求，开始等；
//     任何下行或日志进展都算回包。请求发出后一个字节都没回 → 可疑 / 卡住。
//   - 回合层：日志里回合开始（start 正则）到结束（end 正则）之间算在飞。目标配了 progress 正则时，
//     回合里的进展只认日志新帧，字节只当「线路在动」：线路在动、日志干净、只是还没新帧 → THINKING。
//   - 两层各判一个结论，取更重的。
//   - 不在飞、最近 60 秒也没有动静 → IDLE。「动静」= 发请求、日志事件、等回包时的下行、或一秒 ≥ RequestMinBytes 的下行；
//     保活心跳一类的小包不算，空闲连接字节为 0 更不会触发任何计时。
//   - BUSY（hook 说本地工具在跑）只压住已在活动的目标；BUSY 期间计时暂停，工具结束接着原来的计时。
//   - 目标的任一数据源读不到 → UNKNOWN，reason 写清是哪个、为什么。
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/config"
	"github.com/thoerwink8/fluxwatch/internal/ledger"
	"github.com/thoerwink8/fluxwatch/internal/mihomo"
	"github.com/thoerwink8/fluxwatch/internal/model"
	"github.com/thoerwink8/fluxwatch/internal/procattr"
	"github.com/thoerwink8/fluxwatch/internal/source"
)

const (
	// RequestMinBytes 是一秒内上行多少字节算「发了请求」，下行多少算「在活动」。
	// 实测 mirasim relay 的保活心跳每 30 秒一次、上行 863 / 下行 658 字节，真请求是几 KB 到几十 KB。
	RequestMinBytes = 1024
	// IdleAfter 是没有在飞的请求、也没有动静多久后回到 IDLE。
	IdleAfter = 60 * time.Second
	// TraceLen 是示波器保留的点数（15 分钟，每秒一点）。
	TraceLen = 15 * 60
	// minIntervals 是自适应阈值至少要攒多少个进展间隔才生效。
	minIntervals = 20
	// maxIntervals 是每个节点保留的进展间隔个数。
	maxIntervals = 200
)

// ProcLookup 是端口 → 进程反查（procattr.Attributor 实现它；测试可以换假的）。
type ProcLookup interface {
	Lookup(ports []int) (map[int]procattr.Proc, error)
}

// Options 是引擎的依赖；空着的用默认值（~/.fluxwatch 下的文件、真时钟、真 mihomo、真反查）。
type Options struct {
	ConfigPath   string
	LedgerPath   string
	HookPath     string
	SettingsPath string // 界面的 settings.json；手填的 mihomo 地址和阈值优先
	Interval     time.Duration
	Now          func() time.Time
	Procs        ProcLookup
	Locate       func(ctx context.Context, m mihomo.Manual) (mihomo.Endpoint, error)
}

// Engine 实现 model.Provider。
type Engine struct {
	opts Options

	mu       sync.Mutex // 保护下面的判定状态；一轮 Tick 全程持有
	cfg      *config.Store
	settings *config.SettingsWatcher
	ledger   *ledger.Ledger
	hook     *source.Hook
	client   *mihomo.Client
	manual   mihomo.Manual
	conns    map[string]*connState
	baseline bool
	targets  map[string]*targetState
	order    []string
	lastTick time.Time
	setupErr []string // 启动时的问题（账本坏行等），一直挂在 SourceErr 里直到解决

	snapMu sync.RWMutex
	snap   model.Snapshot
	subs   map[int]chan model.Snapshot
	nextID int
}

type connState struct {
	up, down   int64
	lastChange time.Time
	host       string
}

// New 读配置（第一次写默认）、打开账本，不起循环；调 Run 或手动 Tick。
func New(opts Options) (*Engine, error) {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Locate == nil {
		opts.Locate = mihomo.Locate
	}
	if opts.Procs == nil {
		opts.Procs = procattr.New()
	}
	dir := ""
	if opts.ConfigPath == "" || opts.LedgerPath == "" || opts.HookPath == "" || opts.SettingsPath == "" {
		d, err := config.Dir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	def := func(p *string, name string) {
		if *p == "" {
			*p = filepath.Join(dir, name)
		}
	}
	def(&opts.ConfigPath, "targets.yaml")
	def(&opts.LedgerPath, "ledger.jsonl")
	def(&opts.HookPath, "hook.jsonl")
	def(&opts.SettingsPath, "settings.json")

	store, err := config.Open(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		opts:     opts,
		cfg:      store,
		settings: config.NewSettingsWatcher(opts.SettingsPath),
		hook:     source.NewHook(opts.HookPath),
		conns:    map[string]*connState{},
		targets:  map[string]*targetState{},
		subs:     map[int]chan model.Snapshot{},
	}
	l, err := ledger.Open(opts.LedgerPath, opts.Now())
	if l == nil {
		return nil, err
	}
	if err != nil {
		e.setupErr = append(e.setupErr, err.Error())
	}
	e.ledger = l
	e.applyConfig(store.Get())
	e.snap = model.Snapshot{At: opts.Now(), Worst: model.StateIdle}
	return e, nil
}

// Run 每 Interval 跑一轮，直到 ctx 结束；结束时把没收尾的卡住写进账本。
func (e *Engine) Run(ctx context.Context) {
	tk := time.NewTicker(e.opts.Interval)
	defer tk.Stop()
	e.Tick()
	for {
		select {
		case <-ctx.Done():
			e.Close()
			return
		case <-tk.C:
			e.Tick()
		}
	}
}

// Close 把还开着的卡住记进账本（没恢复），关掉日志文件。
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.opts.Now()
	for _, t := range e.targets {
		t.closeStall(e.ledger, now, false)
		t.closeLogs()
	}
}

// applyConfig 按新配置重建目标；同名、数据源没变的目标保留状态。调用方持锁（或在 New 里）。
func (e *Engine) applyConfig(c *config.Config) {
	next := map[string]*targetState{}
	var order []string
	for _, tc := range c.Targets {
		old := e.targets[tc.Name]
		if old != nil && reflect.DeepEqual(old.cfg.Sources, tc.Sources) {
			old.cfg = tc
			next[tc.Name] = old
		} else {
			if old != nil {
				old.closeLogs()
			}
			next[tc.Name] = newTargetState(tc)
		}
		order = append(order, tc.Name)
	}
	for name, t := range e.targets {
		if _, ok := next[name]; !ok {
			t.closeStall(e.ledger, e.opts.Now(), false)
			t.closeLogs()
		}
	}
	e.targets, e.order = next, order
}

// Tick 跑一轮判定，产出并推送一份 Snapshot。测试直接调它，时间由 Options.Now 决定。
func (e *Engine) Tick() model.Snapshot {
	e.mu.Lock()
	snap := e.tickLocked()
	e.mu.Unlock()
	e.publish(snap)
	return snap
}

func (e *Engine) tickLocked() model.Snapshot {
	now := e.opts.Now()
	dt := time.Duration(0)
	if !e.lastTick.IsZero() {
		dt = now.Sub(e.lastTick)
	}
	e.lastTick = now
	var srcErr []string
	srcErr = append(srcErr, e.setupErr...)

	// 配置热加载
	if changed, err := e.cfg.Reload(); err != nil {
		srcErr = append(srcErr, "配置："+err.Error()+"（沿用上一份）")
	} else if changed {
		e.applyConfig(e.cfg.Get())
	}
	cfg := e.cfg.Get()
	ui, uerr := e.settings.Get()
	if uerr != nil {
		srcErr = append(srcErr, "设置："+uerr.Error())
	}

	// mihomo 一轮读一次，所有目标共用
	conns, merr := e.fetch(cfg, ui)
	if merr != nil {
		srcErr = append(srcErr, "mihomo："+merr.Error())
	}
	deltas := e.updateConns(now, conns, merr == nil)

	// 只给被盯的连接反查进程
	procs := map[int]procattr.Proc{}
	var ports []int
	for _, c := range conns {
		if e.watched(c.HostName()) {
			if p, err := strconv.Atoi(c.Metadata.SourcePort); err == nil {
				ports = append(ports, p)
			}
		}
	}
	if len(ports) > 0 {
		m, err := e.opts.Procs.Lookup(ports)
		if err != nil && !errors.Is(err, procattr.ErrUnsupported) {
			srcErr = append(srcErr, "进程归属："+err.Error()+"（软件名留空，不影响判定）")
		}
		if m != nil {
			procs = m
		}
	}

	busy, herr := e.hook.Busy(now)
	if herr != nil {
		srcErr = append(srcErr, "hook："+herr.Error()+"（不判本地忙）")
	}

	baseSuspect, baseStall := cfg.Defaults.Suspect.Std(), cfg.Defaults.Stall.Std()
	if s, st := ui.Thresholds(); s > 0 && st > s {
		baseSuspect, baseStall = s, st
	}

	snap := model.Snapshot{At: now, Worst: model.StateIdle, Targets: []model.Target{}}
	for _, name := range e.order {
		t := e.targets[name]
		in := tickInput{
			now: now, dt: dt, busy: busy, mihomoErr: merr,
			conns: conns, deltas: deltas, procs: procs, connMeta: e.conns,
			suspect: baseSuspect, stall: baseStall,
		}
		mt, lerr := t.tick(in, e.ledger)
		if lerr != nil {
			srcErr = append(srcErr, "账本："+lerr.Error())
		}
		for _, le := range t.logErrs {
			srcErr = append(srcErr, name+"："+le)
		}
		snap.Targets = append(snap.Targets, mt)
		if mt.State.Severity() > snap.Worst.Severity() {
			snap.Worst = mt.State
		}
		switch mt.State {
		case model.StateSuspect, model.StateStall, model.StateUnknown:
			snap.Alerting++
		}
	}
	snap.SourceErr = dedupe(srcErr)
	return snap
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// watched 判断 host 有没有被某个目标的 mihomo 数据源盯着。
func (e *Engine) watched(host string) bool {
	for _, t := range e.targets {
		if t.matches(host) {
			return true
		}
	}
	return false
}

// fetch 读 /connections；接口没找到或失败时下一轮重新找（核心重启 socket 路径会变）。
func (e *Engine) fetch(cfg *config.Config, ui *config.UISettings) ([]mihomo.Connection, error) {
	man := mihomo.Manual{Addr: cfg.Mihomo.Controller, Secret: cfg.Mihomo.Secret}
	if addr, sec := ui.Manual(); addr != "" { // 界面里手填的优先
		man = mihomo.Manual{Addr: addr, Secret: sec}
	}
	if man != e.manual {
		e.client, e.manual = nil, man
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e.client == nil {
		ep, err := e.opts.Locate(ctx, man)
		if err != nil {
			return nil, err
		}
		e.client = mihomo.New(ep)
	}
	c, err := e.client.Connections(ctx)
	if err != nil {
		err = fmt.Errorf("%s：%w", e.client.Endpoint(), err)
		e.client = nil
		return nil, err
	}
	return c.Connections, nil
}

type delta struct{ up, down int64 }

// updateConns 算每条连接这一轮的字节增量。第一次读到时只建基线；之后新出现的连接全部字节算增量。
func (e *Engine) updateConns(now time.Time, conns []mihomo.Connection, ok bool) map[string]delta {
	d := map[string]delta{}
	if !ok {
		return d
	}
	seen := map[string]bool{}
	for _, c := range conns {
		seen[c.ID] = true
		st := e.conns[c.ID]
		if st == nil {
			// 建基线时已有的连接不知道上次动是什么时候，从开始观测算起（宁可少报 idle 时长）
			st = &connState{lastChange: now, host: c.HostName()}
			if e.baseline {
				st.lastChange = c.Start
				d[c.ID] = delta{up: c.Upload, down: c.Download}
				if c.Upload > 0 || c.Download > 0 {
					st.lastChange = now
				}
			}
			st.up, st.down = c.Upload, c.Download
			e.conns[c.ID] = st
			continue
		}
		du, dd := c.Upload-st.up, c.Download-st.down
		if du < 0 {
			du = 0
		}
		if dd < 0 {
			dd = 0
		}
		if du > 0 || dd > 0 {
			st.lastChange = now
		}
		st.up, st.down = c.Upload, c.Download
		d[c.ID] = delta{up: du, down: dd}
	}
	for id := range e.conns {
		if !seen[id] {
			delete(e.conns, id)
		}
	}
	e.baseline = true
	return d
}

// Snapshot 返回最近一份。
func (e *Engine) Snapshot() model.Snapshot {
	e.snapMu.RLock()
	defer e.snapMu.RUnlock()
	return e.snap
}

// Subscribe 每轮推一份；慢的订阅者只拿最新的。
func (e *Engine) Subscribe() (<-chan model.Snapshot, func()) {
	e.snapMu.Lock()
	defer e.snapMu.Unlock()
	id := e.nextID
	e.nextID++
	ch := make(chan model.Snapshot, 1)
	e.subs[id] = ch
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			e.snapMu.Lock()
			delete(e.subs, id)
			e.snapMu.Unlock()
		})
	}
}

func (e *Engine) publish(s model.Snapshot) {
	e.snapMu.Lock()
	defer e.snapMu.Unlock()
	e.snap = s
	for _, ch := range e.subs {
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- s:
		default:
		}
	}
}

// AddTarget 写进 targets.yaml 并立即生效。
func (e *Engine) AddTarget(label, hostPattern string) error {
	if _, err := e.cfg.AddTarget(label, hostPattern); err != nil {
		return err
	}
	e.mu.Lock()
	e.applyConfig(e.cfg.Get())
	e.mu.Unlock()
	return nil
}

// RemoveTarget 从 targets.yaml 删掉并立即生效。
func (e *Engine) RemoveTarget(name string) error {
	if err := e.cfg.RemoveTarget(name); err != nil {
		return err
	}
	e.mu.Lock()
	e.applyConfig(e.cfg.Get())
	e.mu.Unlock()
	return nil
}

// Diagnose 是「复制诊断」的文本：节点、时长、状态码、计数，不含会话内容。
func (e *Engine) Diagnose(name string) string {
	snap := e.Snapshot()
	e.mu.Lock()
	t := e.targets[name]
	var errs map[string]int
	var acct int
	var sus, st time.Duration
	if t != nil {
		errs, acct = t.recentCounts(snap.At, time.Hour)
		sus, st = t.lastSuspect, t.lastStall
	}
	e.mu.Unlock()
	var mt *model.Target
	for i := range snap.Targets {
		if snap.Targets[i].Name == name {
			mt = &snap.Targets[i]
		}
	}
	if mt == nil {
		return fmt.Sprintf("Fluxwatch 诊断：没有叫 %s 的目标", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Fluxwatch 诊断 %s\n", snap.At.Format("2006-01-02 15:04:05 -0700"))
	fmt.Fprintf(&b, "目标：%s（%s）\n", mt.Label, mt.Name)
	fmt.Fprintf(&b, "状态：%s", mt.State)
	if mt.Layer != "" {
		fmt.Fprintf(&b, "  层：%s", mt.Layer)
	}
	if mt.StuckFor > 0 {
		fmt.Fprintf(&b, "  没进展：%.0fs", mt.StuckFor)
	}
	b.WriteString("\n")
	if mt.Reason != "" {
		fmt.Fprintf(&b, "原因：%s\n", mt.Reason)
	}
	fmt.Fprintf(&b, "节点：%s  软件：%s  连接数：%d\n", orDash(mt.Node), orDash(strings.Join(mt.Apps, ", ")), len(mt.Conns))
	fmt.Fprintf(&b, "阈值：可疑 %s / 卡住 %s\n", sus, st)
	fmt.Fprintf(&b, "今天在这个节点上卡住：%d 次\n", mt.TodayCount)
	if len(errs) > 0 || acct > 0 {
		fmt.Fprintf(&b, "近 1 小时日志：%s", formatCodes(errs))
		if acct > 0 {
			fmt.Fprintf(&b, " 配额线索 ×%d", acct)
		}
		b.WriteString("\n")
	}
	for _, s := range snap.SourceErr {
		fmt.Fprintf(&b, "数据源问题：%s\n", s)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func formatCodes(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

var _ model.Provider = (*Engine)(nil)
