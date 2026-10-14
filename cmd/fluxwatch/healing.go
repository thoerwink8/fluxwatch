package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// retryEvery 是引擎起不来时隔多久再试一次。
const retryEvery = 5 * time.Second

// runner 是真引擎要的那一点：model.Provider 加上「跑起来」。*engine.Engine 实现它。
type runner interface {
	model.Provider
	Run(ctx context.Context)
}

// healing 是引擎起不来（比如 targets.yaml 写坏了）时给界面的 Provider：整体报 unknown 并写明原因，
// 每 retryEvery 秒重试一次，起来了就把订阅者接到真引擎上，之后全部转给它。界面只订阅一次，不用重启。
type healing struct {
	start func() (runner, error)
	every time.Duration

	mu     sync.Mutex
	inner  runner
	reason string
	subs   map[int]chan model.Snapshot
	nextID int
	done   chan struct{} // 真引擎的 Run 返回后关
}

func newHealing(start func() (runner, error), every time.Duration, firstErr error) *healing {
	return &healing{start: start, every: every, reason: startReason(firstErr), subs: map[int]chan model.Snapshot{}}
}

// run 一直重试到起来或 ctx 结束；起来后跑真引擎直到 ctx 结束（Run 收尾写账本）。
func (h *healing) run(ctx context.Context) {
	tk := time.NewTicker(h.every)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		r, err := h.start()
		if err != nil {
			h.mu.Lock()
			h.reason = startReason(err)
			h.mu.Unlock()
			h.broadcast(h.Snapshot())
			continue
		}
		h.mu.Lock()
		h.inner = r
		h.done = make(chan struct{})
		done := h.done
		h.mu.Unlock()
		ch, unsub := r.Subscribe()
		go func() { r.Run(ctx); close(done) }()
		for {
			select {
			case <-ctx.Done():
				unsub()
				<-done
				return
			case s := <-ch:
				h.broadcast(s)
			}
		}
	}
}

// wait 等真引擎收尾（没起来过就不用等）。
func (h *healing) wait() {
	h.mu.Lock()
	done := h.done
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (h *healing) current() (runner, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inner, h.reason
}

func (h *healing) broadcast(s model.Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
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

func (h *healing) Snapshot() model.Snapshot {
	r, reason := h.current()
	if r == nil {
		return model.Snapshot{At: time.Now(), Worst: model.StateUnknown, Targets: []model.Target{}, SourceErr: []string{reason}}
	}
	return r.Snapshot()
}

func (h *healing) Subscribe() (<-chan model.Snapshot, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextID
	h.nextID++
	ch := make(chan model.Snapshot, 1)
	h.subs[id] = ch
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			h.mu.Unlock()
		})
	}
}

func (h *healing) Discover() ([]model.Discovered, error) {
	r, reason := h.current()
	if r == nil {
		return nil, errors.New(reason)
	}
	return r.Discover()
}

func (h *healing) AddTarget(label, host string) error {
	r, reason := h.current()
	if r == nil {
		return errors.New(reason)
	}
	return r.AddTarget(label, host)
}

// AddTargetApps 转给真引擎（界面按软件过滤加目标时用）。
func (h *healing) AddTargetApps(label, host string, apps []string) error {
	r, reason := h.current()
	if r == nil {
		return errors.New(reason)
	}
	a, ok := r.(interface {
		AddTargetApps(label, hostPattern string, apps []string) error
	})
	if !ok {
		return errors.New("现在的数据源不支持按软件过滤")
	}
	return a.AddTargetApps(label, host, apps)
}

func (h *healing) RemoveTarget(name string) error {
	r, reason := h.current()
	if r == nil {
		return errors.New(reason)
	}
	return r.RemoveTarget(name)
}

func (h *healing) Diagnose(name string) string {
	r, reason := h.current()
	if r == nil {
		return reason
	}
	return r.Diagnose(name)
}

var yamlLine = regexp.MustCompile(`\bline (\d+)\b`)

// startReason 把引擎起不来的错误写成人话：yaml 报了行号就写「配置第 N 行有错」，配置的其他错写「配置有错」。
// 面板把第一个「：」前当标题。
func startReason(err error) string {
	if err == nil {
		return "引擎还没起来"
	}
	msg := err.Error()
	tail := "（改好后 " + retryEvery.String() + " 内自动起来）"
	if m := yamlLine.FindStringSubmatch(msg); m != nil {
		return "配置第 " + m[1] + " 行有错：" + msg + tail
	}
	if strings.Contains(msg, "targets.yaml") {
		return "配置有错：" + msg + tail
	}
	return "引擎起不来：" + msg + "（" + retryEvery.String() + " 后重试）"
}
