package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/source"
)

// Settings 是设置窗里的开关，存 ~/.fluxwatch/settings.json。
// 引擎接进来之前，mihomo 接口和阈值只存不用；开机启动和通知由界面自己用。
type Settings struct {
	LaunchAtLogin bool   `json:"launchAtLogin"`
	ShowMenuBar   bool   `json:"showMenuBar"`  // 菜单栏 / 托盘图标（Windows 上固定开）
	ShowDock      bool   `json:"showDock"`     // Dock 图标（只有 mac）
	PriorityIcon  bool   `json:"priorityIcon"` // 菜单栏图标抢占靠右的位置，不被刘海挡（只有 mac）
	Notify        bool   `json:"notify"`
	MihomoMode    string `json:"mihomoMode"`   // "auto" | "manual"
	MihomoAddr    string `json:"mihomoAddr"`   // 手填时：TCP 控制器地址，如 127.0.0.1:9090
	MihomoSecret  string `json:"mihomoSecret"` // 手填时：secret；只存本机文件，不进日志
	SuspectSec    int    `json:"suspectSec"`   // 高级：可疑阈值下限（秒）
	StallSec      int    `json:"stallSec"`     // 高级：卡住阈值下限（秒）
}

// DefaultSettings 和 design.md 第四节的默认阈值一致。
func DefaultSettings() Settings {
	return Settings{ShowMenuBar: true, ShowDock: true, Notify: true, MihomoMode: "auto", SuspectSec: 10, StallSec: 30}
}

func (s Settings) validate() error {
	if !s.ShowMenuBar && !s.ShowDock {
		return fmt.Errorf("菜单栏图标和 Dock 图标至少留一个，不然没有入口")
	}
	if s.MihomoMode != "auto" && s.MihomoMode != "manual" {
		return fmt.Errorf("mihomo 接口只能是自动或手填")
	}
	if s.MihomoMode == "manual" && strings.TrimSpace(s.MihomoAddr) == "" {
		return fmt.Errorf("手填 mihomo 接口时要填地址")
	}
	if s.SuspectSec < 1 || s.StallSec < 1 {
		return fmt.Errorf("阈值要大于 0 秒")
	}
	if s.StallSec <= s.SuspectSec {
		return fmt.Errorf("卡住阈值要大于可疑阈值")
	}
	return nil
}

// settingsStore 读写设置文件。
type settingsStore struct {
	mu   sync.Mutex
	path string
	cur  Settings
}

// fluxwatchDir 是 ~/.fluxwatch（Windows 上是 %USERPROFILE%\.fluxwatch）。
func fluxwatchDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fluxwatch"), nil
}

func newSettingsStore(path string) *settingsStore {
	st := &settingsStore{path: path, cur: DefaultSettings()}
	return st
}

// load 读文件；文件不存在用默认值，读坏了返回错误且保留默认值（不悄悄覆盖用户的文件）。
func (st *settingsStore) load() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, err := os.ReadFile(st.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s := DefaultSettings()
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%s 不是合法的 JSON：%w", filepath.Base(st.path), err)
	}
	st.cur = s
	return nil
}

func (st *settingsStore) get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur
}

// save 校验后原子写回（先写临时文件再改名）。
func (st *settingsStore) save(s Settings) error {
	if err := s.validate(); err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", st.path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	st.cur = s
	return nil
}

// HookStatus 是设置窗里「hook 安装状态」那一行。
type HookStatus struct {
	Installed bool     `json:"installed"` // PreToolUse 和 PostToolUse 都对所有工具挂上了
	Detail    string   `json:"detail"`    // 一句话：看了哪里、看到什么
	LastSeen  string   `json:"lastSeen"`  // hook.jsonl 最后写入时间；没有为空
	Missing   []string `json:"missing"`   // README 里一起装、但没挂的事件（source.ExtraEvents）
	Binary    string   `json:"binary"`    // hook 命令调用的 fluxwatch-cli（展开过 $HOME）；取不到为空
	BinaryOK  bool     `json:"binaryOK"`  // Binary 在且是个文件
}

// hookStatus 看 Claude Code 的用户设置里有没有照 README 挂上 fluxwatch 的 hook（规则在 source.CheckHookInstall），
// 命令指向的 fluxwatch-cli 在不在，以及 hook.jsonl 最近有没有写入。只解析 hooks 那一段，不读出、不保存别的内容。
func hookStatus(home string) HookStatus {
	var hs HookStatus
	cfg := filepath.Join(home, ".claude", "settings.json")
	b, err := os.ReadFile(cfg)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		hs.Detail = "没找到 Claude Code 的用户设置文件"
	case err != nil:
		hs.Detail = "读不到 Claude Code 的用户设置：" + err.Error()
	default:
		in, err := source.CheckHookInstall(b)
		if err != nil {
			hs.Detail = "Claude Code 的用户设置读不懂：" + err.Error()
			break
		}
		hs.Installed = in.Installed()
		hs.Missing = in.Missing
		if in.Binary != "" {
			hs.Binary = source.ExpandHookBinary(in.Binary, home)
			hs.BinaryOK = binaryExists(hs.Binary)
		}
		switch {
		case hs.Installed && hs.Binary != "" && !hs.BinaryOK:
			hs.Detail = "已挂上，但命令指向的 " + hs.Binary + " 不在：hook 什么也记不下（照 README 的写法不会让 Claude Code 报错）"
		case hs.Installed && len(hs.Missing) > 0:
			hs.Detail = "PreToolUse 和 PostToolUse 已挂上；还缺 " + strings.Join(hs.Missing, "、") + "（失败的调用、回合结束要靠它们，片段见 README）"
		case hs.Installed:
			hs.Detail = "工具前后、回合开始结束的事件都已挂上 fluxwatch-cli hook"
		case in.PreOnly || in.PostOnly:
			hs.Detail = "挂了，但 matcher 只限部分工具（要写 *，对所有工具生效）"
		case in.Pre || in.Post:
			hs.Detail = "只挂了一半（PreToolUse 和 PostToolUse 都要挂 fluxwatch-cli hook）"
		default:
			hs.Detail = "还没装：判不了「本地忙」；回合里跑本地工具时分不清是在等工具还是卡住，回合内只报慢、不报卡住"
		}
	}
	if fi, err := os.Stat(filepath.Join(home, ".fluxwatch", "hook.jsonl")); err == nil {
		hs.LastSeen = fi.ModTime().Format("2006-01-02 15:04:05")
	}
	return hs
}

// binaryExists 看 hook 命令调用的程序在不在：带目录的查文件，只写了名字的按 PATH 找。
func binaryExists(p string) bool {
	if !strings.ContainsAny(p, `/\`) {
		_, err := exec.LookPath(p)
		return err == nil
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
