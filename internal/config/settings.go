package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UISettings 是界面设置窗存的 ~/.fluxwatch/settings.json 里引擎要用的部分（字段和 internal/app.Settings 一致）。
type UISettings struct {
	MihomoMode   string `json:"mihomoMode"` // "auto" | "manual"
	MihomoAddr   string `json:"mihomoAddr"`
	MihomoSecret string `json:"mihomoSecret"`
	SuspectSec   int    `json:"suspectSec"`
	StallSec     int    `json:"stallSec"`
}

// Manual 返回手填的控制器；没选手填或地址空着返回空。
func (s *UISettings) Manual() (addr, secret string) {
	if s == nil || s.MihomoMode != "manual" || strings.TrimSpace(s.MihomoAddr) == "" {
		return "", ""
	}
	return strings.TrimSpace(s.MihomoAddr), s.MihomoSecret
}

// Thresholds 返回设置里的阈值下限；没填（≤0）的返回 0。
func (s *UISettings) Thresholds() (suspect, stall time.Duration) {
	if s == nil {
		return 0, 0
	}
	if s.SuspectSec > 0 {
		suspect = time.Duration(s.SuspectSec) * time.Second
	}
	if s.StallSec > 0 {
		stall = time.Duration(s.StallSec) * time.Second
	}
	return
}

// SettingsPath 是 settings.json 的位置。
func SettingsPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "settings.json"), nil
}

// SettingsWatcher 按 mtime 重读 settings.json。文件不存在不算错（界面没存过）。
type SettingsWatcher struct {
	mu   sync.Mutex
	path string
	mod  time.Time
	size int64
	cur  *UISettings
	err  error
}

// NewSettingsWatcher 不立即读，第一次 Get 时读。
func NewSettingsWatcher(path string) *SettingsWatcher { return &SettingsWatcher{path: path, size: -1} }

// Get 返回当前设置；文件没有返回 nil, nil；读坏了返回上一份好的和错误。
func (w *SettingsWatcher) Get() (*UISettings, error) {
	if w == nil || w.path == "" {
		return nil, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	fi, err := os.Stat(w.path)
	if errors.Is(err, fs.ErrNotExist) {
		w.cur, w.err, w.size = nil, nil, -1
		return nil, nil
	}
	if err != nil {
		return w.cur, fmt.Errorf("settings.json 读不到：%w", err)
	}
	if fi.ModTime().Equal(w.mod) && fi.Size() == w.size {
		return w.cur, w.err
	}
	w.mod, w.size = fi.ModTime(), fi.Size()
	b, err := os.ReadFile(w.path)
	if err != nil {
		w.err = fmt.Errorf("settings.json 读不到：%w", err)
		return w.cur, w.err
	}
	var s UISettings
	if err := json.Unmarshal(b, &s); err != nil {
		w.err = fmt.Errorf("settings.json 不是合法 JSON：%w", err)
		return w.cur, w.err
	}
	w.cur, w.err = &s, nil
	return w.cur, nil
}
