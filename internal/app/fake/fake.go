// Package fake 是开发和截图用的假 Provider：数据随时间走，覆盖卡住（A 线路停）、
// 慢（可疑）、本地忙、上游 503（B）四种状态。`fluxwatch -fake` 用它。
// 只造字节数、域名、节点名、软件名、状态码，不造会话内容。
package fake

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

const traceLen = 15 * 60 // 15 分钟，每秒一点

// 每个假目标的剧本。
type script int

const (
	scriptStall    script = iota // 跑一阵 → 停 → 卡住（A 线路停）→ 恢复，循环
	scriptSlow                   // 断断续续，静默在 8–24s 之间摆：可疑但到不了卡住
	scriptBusy                   // 本地工具在跑，网络静默属正常
	scriptUpstream               // 字节偶有、日志 503 一直在冒：B 上游卡
	scriptHealthy                // 新加入监控的目标：正常
)

type target struct {
	name, label, host, node string
	apps                    []string
	script                  script
	trace                   []model.Sample
	stuck                   float64 // 秒：当前静默多久
	phaseLeft               float64 // 剧本当前阶段还剩多久
	running                 bool    // 剧本 stall：true 表示在跑、false 表示在停
	conns                   []model.Conn
	today                   int
	errN                    int
}

// Provider 是假的 model.Provider。
type Provider struct {
	mu      sync.Mutex
	rnd     *rand.Rand
	targets []*target
	subs    map[int]chan model.Snapshot
	nextSub int
	last    model.Snapshot
	extra   []model.Discovered // 发现面板里还没被盯的
}

// New 造一个假 Provider 并开始每秒走一步。
func New() *Provider {
	p := &Provider{rnd: rand.New(rand.NewSource(7)), subs: map[int]chan model.Snapshot{}}
	now := time.Now().Truncate(time.Second)
	p.targets = []*target{
		{name: "reclaude", label: "ReClaude", host: "misaka.route.reclaude.ai", node: "日本专线04",
			apps: []string{"Claude Code"}, script: scriptStall, stuck: 42, running: false, phaseLeft: 48, today: 3},
		{name: "mirasim", label: "Mirasim Cloud", host: "relay.mirasim.ai", node: "日本专线04",
			apps: []string{"Mirasim"}, script: scriptSlow, stuck: 12, phaseLeft: 9, today: 1},
		{name: "claude-code", label: "Claude Code 本体", host: "api.anthropic.com", node: "DIRECT",
			apps: []string{"Claude Code"}, script: scriptBusy},
		{name: "gateway", label: "API 网关", host: "gw.example.com", node: "香港中继02",
			apps: []string{"curl"}, script: scriptUpstream, stuck: 34, errN: 4},
	}
	for _, t := range p.targets {
		t.trace = p.history(t, now)
		t.conns = p.makeConns(t, now)
	}
	p.extra = []model.Discovered{
		{Host: "cdn.example.com", Suggest: "*.example.com", Node: "日本高速03", Apps: []string{"Safari"}, ConnN: 1, Down: 2_600_000},
		{Host: "api.github.com", Suggest: "*.github.com", Node: "日本高速03", Apps: []string{"Mirasim"}, ConnN: 1, Down: 47_000},
		{Host: "auth.mirasim.ai", Suggest: "*.mirasim.ai", Node: "日本高速03", Apps: []string{"Mirasim"}, ConnN: 1, Down: 5_000},
		{Host: "updates.example.org", Suggest: "*.example.org", Node: "DIRECT", Apps: []string{"Example Updater"}, ConnN: 1, Down: 37_000},
		{Host: "static.example.net", Suggest: "*.example.net", Node: "日本高速03", Apps: []string{"Arc"}, ConnN: 2, Down: 27_000},
	}
	p.last = p.build(now)
	go p.loop()
	return p
}

// history 造 15 分钟的历史轨迹，最后 stuck 秒是 0。
func (p *Provider) history(t *target, now time.Time) []model.Sample {
	out := make([]model.Sample, traceLen)
	for i := range out {
		at := now.Add(time.Duration(i-traceLen+1) * time.Second)
		age := float64(traceLen - 1 - i)
		var d int64
		switch t.script {
		case scriptBusy:
			d = 0
			if age > 300 && p.rnd.Float64() < 0.5 {
				d = p.burst(0.6)
			}
		case scriptUpstream:
			if p.rnd.Float64() < 0.35 {
				d = p.burst(0.5)
			}
		default:
			d = p.burst(1)
		}
		if age < t.stuck {
			d = 0
		}
		out[i] = model.Sample{At: at, Down: d, Up: d / 40}
	}
	return out
}

func (p *Provider) burst(scale float64) int64 {
	// 流式回复：几 KB 到几十 KB 一秒，偶尔一个尖峰
	v := 2_000 + p.rnd.ExpFloat64()*14_000
	if p.rnd.Float64() < 0.08 {
		v *= 2.5
	}
	return int64(v * scale)
}

func (p *Provider) makeConns(t *target, now time.Time) []model.Conn {
	n := 2 + p.rnd.Intn(2)
	out := make([]model.Conn, 0, n)
	for i := 0; i < n; i++ {
		c := model.Conn{
			ID: fmt.Sprintf("%s-%d", t.name, i), Host: t.host, Node: t.node, PID: 4000 + p.rnd.Intn(5000),
			Up: int64(20_000 + p.rnd.Intn(80_000)), Down: int64(200_000 + p.rnd.Intn(3_000_000)),
			Started: now.Add(-time.Duration(60+p.rnd.Intn(900)) * time.Second),
		}
		if len(t.apps) > 0 {
			c.App = t.apps[0]
		}
		// 主连接的静默就是目标卡住的时长；其余几条是早先停下的旁支
		c.IdleFor = t.stuck
		if i > 0 {
			c.IdleFor = t.stuck + float64(3+p.rnd.Intn(40))
		}
		out = append(out, c)
	}
	return out
}

func (p *Provider) loop() {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for now := range tk.C {
		p.mu.Lock()
		p.step(now.Truncate(time.Second))
		snap := p.build(now)
		p.last = snap
		subs := make([]chan model.Snapshot, 0, len(p.subs))
		for _, c := range p.subs {
			subs = append(subs, c)
		}
		p.mu.Unlock()
		for _, c := range subs {
			select {
			case c <- snap:
			default: // 订阅方没读完就丢这一份，下一秒还有
			}
		}
	}
}

// step 让每个目标按剧本走一秒。
func (p *Provider) step(now time.Time) {
	for _, t := range p.targets {
		var d int64
		switch t.script {
		case scriptStall:
			t.phaseLeft--
			if t.running {
				d = p.burst(1)
				t.stuck = 0
				if t.phaseLeft <= 0 {
					t.running, t.phaseLeft = false, 70+float64(p.rnd.Intn(30))
				}
			} else {
				t.stuck++
				if t.phaseLeft <= 0 {
					t.running, t.phaseLeft = true, 40+float64(p.rnd.Intn(30))
					t.today++
				}
			}
		case scriptSlow:
			t.phaseLeft--
			if t.phaseLeft <= 0 {
				d = p.burst(0.7)
				t.stuck = 0
				t.phaseLeft = 8 + float64(p.rnd.Intn(16))
			} else {
				t.stuck++
			}
		case scriptBusy:
			t.stuck = 0
		case scriptUpstream:
			t.stuck++
			if p.rnd.Float64() < 0.1 {
				t.errN++
			}
			if t.stuck > 90 { // 偶尔漏出一点，重新计时
				d = p.burst(0.4)
				t.stuck = 0
			}
		case scriptHealthy:
			if p.rnd.Float64() < 0.8 {
				d = p.burst(0.8)
				t.stuck = 0
			} else {
				t.stuck++
			}
		}
		t.trace = append(t.trace[1:], model.Sample{At: now, Down: d, Up: d / 40})
		for i := range t.conns {
			c := &t.conns[i]
			if i == 0 && d > 0 {
				c.Down += d
				c.Up += d / 40
				c.IdleFor = 0
			} else {
				c.IdleFor++
			}
		}
	}
}

func (t *target) state() (model.State, model.Layer, string) {
	switch t.script {
	case scriptBusy:
		return model.StateBusy, model.LayerNone, "hook：本地工具在跑，静默不判定"
	case scriptUpstream:
		if t.stuck >= 30 {
			return model.StateStall, model.LayerUpstream, fmt.Sprintf("gateway.log 503 ×%d", t.errN)
		}
		if t.stuck >= 10 {
			return model.StateSuspect, model.LayerNone, fmt.Sprintf("%.0fs 无进展", t.stuck)
		}
		return model.StateOK, model.LayerNone, "有进展"
	}
	switch {
	case t.stuck >= 30:
		return model.StateStall, model.LayerLine, "线路字节全停、日志无错（节点 " + t.node + "）"
	case t.stuck >= 10:
		return model.StateSuspect, model.LayerNone, fmt.Sprintf("%.0fs 无进展", t.stuck)
	case t.stuck >= 3:
		return model.StateThinking, model.LayerNone, "线路在动、日志干净"
	default:
		return model.StateOK, model.LayerNone, "有进展"
	}
}

func (p *Provider) build(now time.Time) model.Snapshot {
	s := model.Snapshot{At: now, Worst: model.StateIdle}
	for _, t := range p.targets {
		st, layer, reason := t.state()
		tg := model.Target{
			Name: t.name, Label: t.label, State: st, Layer: layer, Reason: reason,
			Node: t.node, Apps: append([]string(nil), t.apps...),
			Conns:      append([]model.Conn(nil), t.conns...),
			Trace:      append([]model.Sample(nil), t.trace...),
			TodayCount: t.today,
		}
		if st == model.StateSuspect || st == model.StateStall || st == model.StateThinking {
			tg.StuckFor = t.stuck
		}
		if st.Severity() > s.Worst.Severity() {
			s.Worst = st
		}
		switch st {
		case model.StateSuspect, model.StateStall, model.StateUnknown:
			s.Alerting++
		}
		s.Targets = append(s.Targets, tg)
	}
	return s
}

// Snapshot 实现 model.Provider。
func (p *Provider) Snapshot() model.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// Subscribe 实现 model.Provider。
func (p *Provider) Subscribe() (<-chan model.Snapshot, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextSub
	p.nextSub++
	c := make(chan model.Snapshot, 1)
	p.subs[id] = c
	var once sync.Once
	return c, func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.subs, id)
			p.mu.Unlock()
		})
	}
}

// Discover 实现 model.Provider：已盯的目标也列出来，标 Watched。
func (p *Provider) Discover() ([]model.Discovered, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []model.Discovered
	for _, t := range p.targets {
		if t.node == "DIRECT" && t.script == scriptBusy {
			continue
		}
		var down int64
		for _, c := range t.conns {
			down += c.Down
		}
		out = append(out, model.Discovered{Host: t.host, Suggest: Suggest(t.host), Node: t.node,
			Apps: t.apps, ConnN: len(t.conns), Down: down, Watched: true})
	}
	for _, d := range p.extra {
		jitter := 1 + 0.1*math.Sin(float64(time.Now().Unix())/7)
		d.Down = int64(float64(d.Down) * jitter)
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Down > out[j].Down })
	return out, nil
}

// Suggest 给一个域名建议通配符：去掉最左一段（两段的域名原样加 *.）。
func Suggest(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return "*." + host
	}
	return "*." + strings.Join(parts[len(parts)-2:], ".")
}

// AddTargetApps 同 AddTarget；演示数据不按软件过滤，只把软件名记到目标上。
func (p *Provider) AddTargetApps(label, hostPattern string, apps []string) error {
	if err := p.AddTarget(label, hostPattern); err != nil {
		return err
	}
	if len(apps) > 0 {
		p.mu.Lock()
		p.targets[len(p.targets)-1].apps = append([]string(nil), apps...)
		p.mu.Unlock()
	}
	return nil
}

// AddTarget 实现 model.Provider：加一个正常态的目标，发现面板里对应的行变成已盯。
func (p *Provider) AddTarget(label, hostPattern string) error {
	label = strings.TrimSpace(label)
	hostPattern = strings.TrimSpace(hostPattern)
	if label == "" || hostPattern == "" {
		return fmt.Errorf("名字和域名都不能为空")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	name := strings.ToLower(strings.ReplaceAll(label, " ", "-"))
	for _, t := range p.targets {
		if t.name == name {
			return fmt.Errorf("已经有叫 %s 的目标", label)
		}
	}
	host := strings.TrimPrefix(hostPattern, "*.")
	node := "日本高速03"
	apps := []string{}
	keep := p.extra[:0]
	for _, d := range p.extra {
		if matches(hostPattern, d.Host) {
			host, node, apps = d.Host, d.Node, d.Apps
			continue
		}
		keep = append(keep, d)
	}
	p.extra = keep
	t := &target{name: name, label: label, host: host, node: node, apps: apps, script: scriptHealthy}
	now := time.Now().Truncate(time.Second)
	t.trace = p.history(t, now)
	t.conns = p.makeConns(t, now)
	p.targets = append(p.targets, t)
	return nil
}

func matches(pattern, host string) bool {
	if strings.HasPrefix(pattern, "*.") {
		suf := pattern[1:]
		return strings.HasSuffix(host, suf) || host == pattern[2:]
	}
	return pattern == host
}

// RemoveTarget 实现 model.Provider。
func (p *Provider) RemoveTarget(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, t := range p.targets {
		if t.name == name {
			p.targets = append(p.targets[:i], p.targets[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("没有叫 %s 的目标", name)
}

// Diagnose 实现 model.Provider：只有节点、时长、状态码、计数。
func (p *Provider) Diagnose(name string) string {
	snap := p.Snapshot()
	for _, t := range snap.Targets {
		if t.Name != name {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Fluxwatch 诊断（演示数据） %s\n", snap.At.Format(time.RFC3339))
		fmt.Fprintf(&b, "目标：%s（%s）\n状态：%s", t.Label, t.Name, t.State)
		if t.Layer != model.LayerNone {
			fmt.Fprintf(&b, "  层：%s", t.Layer)
		}
		fmt.Fprintf(&b, "\n原因：%s\n无进展：%.0fs\n节点：%s\n软件：%s\n今天在此节点卡住：%d 次\n连接：\n",
			t.Reason, t.StuckFor, t.Node, strings.Join(t.Apps, ", "), t.TodayCount)
		for _, c := range t.Conns {
			fmt.Fprintf(&b, "  %s  %s  ↓%d ↑%d  静默 %.0fs\n", c.Host, c.Node, c.Down, c.Up, c.IdleFor)
		}
		return b.String()
	}
	return "没有这个目标：" + name
}

var _ model.Provider = (*Provider)(nil)
