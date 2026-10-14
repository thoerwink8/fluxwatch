package app

import (
	"sync"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// 示波器要看 1 小时，而 Snapshot 只带 15 分钟：界面这边按目标攒一个 1 小时的环。
const historyLen = 60 * 60

type history struct {
	mu   sync.Mutex
	byID map[string][]model.Sample
}

func newHistory() *history { return &history{byID: map[string][]model.Sample{}} }

// merge 把快照里比已有最后一点更新的样本接上去，超出 1 小时的丢掉；快照里没了的目标清掉。
func (h *history) merge(s model.Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	for _, t := range s.Targets {
		seen[t.Name] = true
		cur := h.byID[t.Name]
		var last time.Time
		if len(cur) > 0 {
			last = cur[len(cur)-1].At
		}
		for _, p := range t.Trace {
			if p.At.After(last) {
				cur = append(cur, p)
			}
		}
		if len(cur) > historyLen {
			cur = append([]model.Sample(nil), cur[len(cur)-historyLen:]...)
		}
		h.byID[t.Name] = cur
	}
	for name := range h.byID {
		if !seen[name] {
			delete(h.byID, name)
		}
	}
}

// last 取某目标最近 seconds 秒的样本（复制一份）。
func (h *history) last(name string, seconds int) []model.Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.byID[name]
	if seconds > 0 && len(cur) > seconds {
		cur = cur[len(cur)-seconds:]
	}
	return append([]model.Sample(nil), cur...)
}
