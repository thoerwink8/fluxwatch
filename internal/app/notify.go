package app

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// alertKind 是要发通知的两种时刻。
type alertKind int

const (
	alertStall   alertKind = iota // 进 STALL
	alertRecover                  // 从 STALL 恢复
)

type alert struct {
	kind   alertKind
	target model.Target
	stuck  float64 // 恢复时：卡了多久（取恢复前最后一次看到的 StuckFor）
}

// transitions 比较上一份和这一份，找出「进 STALL」和「从 STALL 恢复」。每次卡住只发一次「卡住」、最多一次「已恢复」。
// 一次卡住从进 STALL 开始，到引擎标 Recovered（真进展，发「已恢复」）、或者目标转 IDLE / 不带 Recovered 的 OK
// （连接没了、睡眠唤醒一类按未恢复收尾，不发）为止。中间被 THINKING、BUSY、SUSPECT、UNKNOWN 带走再回到 STALL
// 仍是同一次，不再发。目标被删掉不发。
type transitions struct {
	prev  map[string]model.State
	open  map[string]bool // 有一次还没收尾的卡住
	stuck map[string]float64
}

func newTransitions() *transitions {
	return &transitions{prev: map[string]model.State{}, open: map[string]bool{}, stuck: map[string]float64{}}
}

func (tr *transitions) next(s model.Snapshot) []alert {
	var out []alert
	seen := make(map[string]bool, len(s.Targets))
	for _, t := range s.Targets {
		seen[t.Name] = true
		_, known := tr.prev[t.Name]
		switch {
		case t.State == model.StateStall && !tr.open[t.Name]:
			tr.open[t.Name] = true
			if known {
				out = append(out, alert{kind: alertStall, target: t})
			}
		case t.Recovered && tr.open[t.Name]:
			out = append(out, alert{kind: alertRecover, target: t, stuck: tr.stuck[t.Name]})
			tr.open[t.Name] = false
		case t.State == model.StateIdle || t.State == model.StateOK:
			tr.open[t.Name] = false
		}
		tr.prev[t.Name] = t.State
		if t.State == model.StateStall {
			tr.stuck[t.Name] = t.StuckFor
		}
	}
	for name := range tr.prev {
		if !seen[name] {
			delete(tr.prev, name)
			delete(tr.open, name)
			delete(tr.stuck, name)
		}
	}
	return out
}

// shouldNotify：通知开着全发；关着时只有一种例外——图标落在刘海里看不见，卡住仍然发（不然完全看不见），
// 「已恢复」不发。设置窗里写明了这一条。
func shouldNotify(a alert, notifyOn, iconInNotch bool) bool {
	if notifyOn {
		return true
	}
	return iconInNotch && a.kind == alertStall
}

func hasStall(alerts []alert) bool {
	for _, a := range alerts {
		if a.kind == alertStall {
			return true
		}
	}
	return false
}

// 第一份快照里已经在 STALL 的目标不补发（启动时不炸一串通知），从第二份开始算跳变。

var layerText = map[model.Layer]string{
	model.LayerLine:     "A 线路停",
	model.LayerUpstream: "B 上游卡",
	model.LayerAccount:  "C 账号配额",
}

// message 生成通知标题和正文：只有目标名、层、节点、时长，不带会话内容。
func (a alert) message() (title, body string) {
	t := a.target
	switch a.kind {
	case alertStall:
		title = t.Label + " 卡住了"
		body = fmt.Sprintf("%.0fs 没有进展", t.StuckFor)
		if l, ok := layerText[t.Layer]; ok {
			body = l + " · " + body
		}
		if t.Node != "" {
			body += " · " + t.Node
		}
	default:
		title = t.Label + " 已恢复"
		body = fmt.Sprintf("卡了 %.0fs 后恢复", a.stuck)
		if t.Node != "" {
			body += " · " + t.Node
		}
	}
	return title, body
}

// notifier 先用 Wails 的通知服务；拿不到（mac 上没打成 .app、没有 bundle id 时）退到平台原生方式。
type notifier struct {
	mu       sync.Mutex
	svc      *notifications.NotificationService
	ready    bool
	authOK   bool
	fallback func(title, body string) error // 平台原生退路；没有为 nil
	seq      int
}

func newNotifier() *notifier {
	return &notifier{svc: notifications.New(), fallback: nativeNotify}
}

// ServiceName / ServiceStartup 让它作为 Wails 服务随应用启动；内层服务起不来不拖垮应用。
func (n *notifier) ServiceName() string { return "fluxwatch-notifier" }

func (n *notifier) ServiceStartup(ctx context.Context, opts application.ServiceOptions) error {
	if err := n.svc.ServiceStartup(ctx, opts); err != nil {
		log.Printf("通知：Wails 通知服务不可用（%v），改用平台原生方式", err)
		return nil
	}
	n.mu.Lock()
	n.ready = true
	n.mu.Unlock()
	go func() {
		ok, err := n.svc.RequestNotificationAuthorization()
		if err != nil {
			log.Printf("通知：请求授权失败：%v", err)
		}
		n.mu.Lock()
		n.authOK = ok
		n.mu.Unlock()
	}()
	return nil
}

func (n *notifier) ServiceShutdown() error {
	n.mu.Lock()
	ready := n.ready
	n.mu.Unlock()
	if ready {
		return n.svc.ServiceShutdown()
	}
	return nil
}

func (n *notifier) send(title, body string) {
	n.mu.Lock()
	n.seq++
	id := fmt.Sprintf("fluxwatch-%d", n.seq)
	useSvc := n.ready && n.authOK
	n.mu.Unlock()
	if useSvc {
		err := n.svc.SendNotification(notifications.NotificationOptions{ID: id, Title: title, Body: body})
		if err == nil {
			return
		}
		log.Printf("通知：Wails 发送失败（%v），改用平台原生方式", err)
	}
	if n.fallback == nil {
		log.Printf("通知：这个平台没有可用的通知方式，丢弃：%s", title)
		return
	}
	if err := n.fallback(title, body); err != nil {
		log.Printf("通知：平台原生方式也失败：%v", err)
	}
}
