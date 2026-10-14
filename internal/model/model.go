// Package model 是引擎和界面之间的约定：引擎每秒产出一份 Snapshot，界面和命令行只读它。
// 两边并行开发都照这份写，改字段要两边一起改。
package model

import "time"

// State 是一个监控目标此刻的判定。
type State string

const (
	StateIdle     State = "idle"     // 没有在等网络（没请求在飞）
	StateOK       State = "ok"       // 在等网络，而且有进展
	StateThinking State = "thinking" // 线路在动、日志干净、只是还没新帧：放宽阈值
	StateSuspect  State = "suspect"  // 超过 suspect 阈值没进展
	StateStall    State = "stall"    // 超过 stall 阈值没进展
	StateBusy     State = "busy"     // 本地工具在跑，网络静默属正常，不判定
	StateUnknown  State = "unknown"  // 数据源读不到：照实报，不当成正常
)

// Layer 是卡住时判到的那一层（只在 suspect / stall 时有意义）。
type Layer string

const (
	LayerNone     Layer = ""
	LayerLine     Layer = "A" // 线路停：字节冻结、日志无错
	LayerUpstream Layer = "B" // 上游卡：日志里 429 / 5xx
	LayerAccount  Layer = "C" // 账号配额：日志里换号、配额线索
)

// Severity 给菜单栏图标取「最严重的目标」用；busy 和 idle 不参与。
func (s State) Severity() int {
	switch s {
	case StateStall:
		return 4
	case StateUnknown:
		return 3
	case StateSuspect:
		return 2
	case StateOK, StateThinking:
		return 1
	default:
		return 0
	}
}

// Conn 是一条代理连接，来自 mihomo /connections，进程归属由本机补上。
type Conn struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`
	Node      string    `json:"node"`  // chains[0]，出口节点名
	App       string    `json:"app"`   // 上溯到 .app / 可执行文件的软件名；拿不到为空
	PID       int       `json:"pid"`   // 拿不到为 0
	Up        int64     `json:"up"`    // 累计字节
	Down      int64     `json:"down"`  // 累计字节
	IdleFor   float64   `json:"idleFor"` // 秒：多久没有字节增量
	Started   time.Time `json:"started"`
}

// Sample 是示波器上的一点：某目标一秒内的下行增量。
type Sample struct {
	At   time.Time `json:"at"`
	Down int64     `json:"down"`
	Up   int64     `json:"up"`
}

// Target 是一个被盯的目标此刻的全貌。
type Target struct {
	Name      string   `json:"name"`  // 配置里的 name
	Label     string   `json:"label"` // 界面显示名
	State     State    `json:"state"`
	Layer     Layer    `json:"layer"`
	Reason    string   `json:"reason"`    // 一句话：为什么是这个判定（「daemon.log 503 ×4」）
	StuckFor  float64  `json:"stuckFor"`  // 秒：没进展多久（suspect / stall 时）
	Node      string   `json:"node"`      // 当前主要出口节点
	Apps      []string `json:"apps"`      // 发出这些连接的软件
	Conns     []Conn   `json:"conns"`
	Trace     []Sample `json:"trace"`     // 最近 15 分钟，每秒一点
	TodayCount int     `json:"todayCount"` // 今天在这个节点上卡住的次数（账本）
}

// Snapshot 是引擎每秒推给界面的一份。
type Snapshot struct {
	At       time.Time `json:"at"`
	Targets  []Target  `json:"targets"`
	Worst    State     `json:"worst"`    // 菜单栏图标：Severity 最大的那个；都没有就 idle
	Alerting int       `json:"alerting"` // 处于 suspect / stall / unknown 的目标数（角标）
	SourceErr []string `json:"sourceErr"` // 读不到的数据源，照实列出
}

// Discovered 是「发现」面板的一行：此刻正在跑、还没被盯的流量，按域名聚合。
type Discovered struct {
	Host     string   `json:"host"`
	Suggest  string   `json:"suggest"` // 建议的通配符，如 *.reclaude.ai
	Node     string   `json:"node"`
	Apps     []string `json:"apps"`
	ConnN    int      `json:"connN"`
	Down     int64    `json:"down"`
	Watched  bool     `json:"watched"` // 已被某个目标盯着
}

// Provider 是界面依赖的全部能力；引擎实现它，界面开发期可以用假的。
type Provider interface {
	Snapshot() Snapshot
	Subscribe() (<-chan Snapshot, func()) // 每秒一份；第二个返回值退订
	Discover() ([]Discovered, error)
	AddTarget(label, hostPattern string) error // 「加入监控」：写进 targets.yaml 并立即生效
	RemoveTarget(name string) error
	Diagnose(name string) string // 「复制诊断」的文本：不含会话内容，只有节点、时长、状态码、计数
}
