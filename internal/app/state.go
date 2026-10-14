package app

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// stateFile 是托盘每秒写出的此刻快照（~/.fluxwatch/state.json）。`fluxwatch-cli status` 默认读它，
// 读到的就是托盘此刻的真实状态，而不是自己另起一个引擎采两秒。格式：
//
//	{"writtenAt": "...", "pid": 123, "snapshot": {model.Snapshot，不带 15 分钟轨迹}}
//
// 托盘退出不删它：status 按 writtenAt 判新旧（超过 5 秒当托盘没在跑），崩溃退出也一样判得出来。
const stateFile = "state.json"

type stateDoc struct {
	WrittenAt time.Time      `json:"writtenAt"`
	PID       int            `json:"pid"`
	Snapshot  model.Snapshot `json:"snapshot"`
}

// stateWriter 原子写 state.json（先写同目录临时文件再改名）。写失败只记一次日志，恢复后再记一次。
type stateWriter struct {
	mu     sync.Mutex
	path   string
	now    func() time.Time
	failed bool
}

func newStateWriter(path string) *stateWriter {
	return &stateWriter{path: path, now: time.Now}
}

func (w *stateWriter) write(s model.Snapshot) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.writeLocked(s)
	switch {
	case err != nil && !w.failed:
		log.Printf("state.json 写不进去（fluxwatch-cli status 会退回自己采样）：%v", err)
	case err == nil && w.failed:
		log.Printf("state.json 又能写了")
	}
	w.failed = err != nil
}

func (w *stateWriter) writeLocked(s model.Snapshot) error {
	b, err := json.Marshal(stateDoc{WrittenAt: w.now(), PID: os.Getpid(), Snapshot: s})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", w.path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, w.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
