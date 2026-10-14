// Package ledger 是卡住账本 ~/.fluxwatch/ledger.jsonl：每次 STALL 一行，不含会话内容。
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry 是一次卡住。
type Entry struct {
	At          time.Time  `json:"at"`     // 进 STALL 的时间
	Target      string     `json:"target"` // 目标 name
	Layer       string     `json:"layer"`  // A / B / C
	Node        string     `json:"node"`
	Seconds     float64    `json:"seconds"`               // 卡了多久（从没进展算起到离开 STALL）
	RecoveredAt *time.Time `json:"recoveredAt,omitempty"` // 有进展恢复的时间；没恢复（转成 idle / unknown / 程序退出）为空
	Reason      string     `json:"reason,omitempty"`      // 状态码、计数一类，不含会话内容
}

// Ledger 追加写，内存里留最近两天的条目给 TodayCount 用。
type Ledger struct {
	mu      sync.Mutex
	path    string
	entries []Entry
}

// keep 是内存里留多久的条目（够算「今天」，跨时区也够）。
const keep = 48 * time.Hour

// Open 读已有账本。文件不存在不算错；有坏行跳过并返回第一处错误的说明（账本仍可用）。
func Open(path string, now time.Time) (*Ledger, error) {
	l := &Ledger{path: path}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("账本读不到：%w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var bad error
	line := 0
	for sc.Scan() {
		line++
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			if bad == nil {
				bad = fmt.Errorf("账本第 %d 行坏了，已跳过：%w", line, err)
			}
			continue
		}
		if now.Sub(e.At) <= keep {
			l.entries = append(l.entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("账本读不到：%w", err)
	}
	return l, bad
}

// Append 追加一行。
func (l *Ledger) Append(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	l.entries = append(l.entries, e)
	cut := 0
	for cut < len(l.entries) && e.At.Sub(l.entries[cut].At) > keep {
		cut++
	}
	l.entries = l.entries[cut:]
	return nil
}

// TodayCount 是 now 所在本地日期里，这个目标在这个节点上卡住的次数。node 为空时不分节点。
func (l *Ledger) TodayCount(now time.Time, target, node string) int {
	y, m, d := now.Local().Date()
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.entries {
		if e.Target != target || (node != "" && e.Node != node) {
			continue
		}
		ey, em, ed := e.At.Local().Date()
		if ey == y && em == m && ed == d {
			n++
		}
	}
	return n
}
