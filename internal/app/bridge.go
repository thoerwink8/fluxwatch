package app

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// Bridge 是前端能调的全部方法（Wails 绑定）。前端用
// Call.ByName("github.com/thoerwink8/fluxwatch/internal/app.Bridge.<方法>") 调。
type Bridge struct {
	p           model.Provider
	app         *application.App
	panel       *application.WebviewWindow
	tray        *trayController
	store       *settingsStore
	settingsErr error
	hist        *history
	fake        bool
	version     string
	state       *stateWriter
	panelShown  atomic.Bool // 面板此刻可见（WindowShow / WindowHide 事件维护）

	winMu    sync.Mutex
	discover *application.WebviewWindow
	settings *application.WebviewWindow
}

// Info 是前端开场要的几样东西。
type Info struct {
	Fake     bool   `json:"fake"`
	Version  string `json:"version"`
	Platform string `json:"platform"` // darwin / windows
}

func (b *Bridge) Info() Info {
	return Info{Fake: b.fake, Version: b.version, Platform: runtime.GOOS}
}

// Snapshot 取当前一份（不带轨迹）。
func (b *Bridge) Snapshot() model.Snapshot { return lite(b.p.Snapshot()) }

// Trace 取某目标最近 seconds 秒的示波器样本（60 / 900 / 3600）。
func (b *Bridge) Trace(name string, seconds int) []model.Sample {
	if seconds <= 0 || seconds > historyLen {
		seconds = historyLen
	}
	return b.hist.last(name, seconds)
}

// CopyDiagnose 把诊断文本放进剪贴板，返回文本给界面预览。
func (b *Bridge) CopyDiagnose(name string) (string, error) {
	text := b.p.Diagnose(name)
	if !b.app.Clipboard.SetText(text) {
		return text, errors.New("写剪贴板失败")
	}
	return text, nil
}

// OpenLog 打开目标的日志；目标没有日志文件时打开 ~/.fluxwatch（账本和设置都在那）。返回实际打开的路径。
func (b *Bridge) OpenLog(name string) (string, error) {
	var path string
	for _, t := range b.p.Snapshot().Targets {
		if t.Name == name {
			path = t.LogPath
		}
	}
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("日志文件打不开：%w", err)
		}
		// Finder / 资源管理器带到前面后面板失焦自己收起；打不开时面板留着显示原因
		if ok, err := revealPath(path); ok {
			return path, err
		}
		return path, b.app.Env.OpenFileManager(path, true)
	}
	dir, err := fluxwatchDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if ok, err := openDir(dir); ok {
		return dir, err
	}
	return dir, b.app.Browser.OpenFile(dir)
}

// Discover 列此刻正在跑的流量。
func (b *Bridge) Discover() ([]model.Discovered, error) { return b.p.Discover() }

// appsAdder 是能按软件过滤的 Provider（引擎、演示数据都实现）；model.Provider 本身只有 AddTarget。
type appsAdder interface {
	AddTargetApps(label, hostPattern string, apps []string) error
}

// AddTarget 「加入监控」。apps 是发现面板那一行的软件（表单里可删）；不为空时只算这些软件发的连接。
func (b *Bridge) AddTarget(label, hostPattern string, apps []string) error {
	label, hostPattern = strings.TrimSpace(label), strings.TrimSpace(hostPattern)
	if label == "" {
		return errors.New("给目标起个名字")
	}
	if hostPattern == "" || strings.ContainsAny(hostPattern, " /:") {
		return errors.New("域名写成 example.com 或 *.example.com 的样子")
	}
	apps = splitApps(apps)
	if len(apps) == 0 {
		return b.p.AddTarget(label, hostPattern)
	}
	if a, ok := b.p.(appsAdder); ok {
		return a.AddTargetApps(label, hostPattern, apps)
	}
	return errors.New("现在的数据源不支持按软件过滤：把「软件」清空再加")
}

// splitApps 去空白、去空项（表单里删成空的那一项不算）。
func splitApps(in []string) []string {
	var out []string
	for _, a := range in {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// FitPanel 前端量好内容高度后调：改窗口高度并重新贴到托盘下。
func (b *Bridge) FitPanel(height int) {
	if height < panelMinH {
		height = panelMinH
	}
	if height > panelMaxH {
		height = panelMaxH
	}
	_, h := b.panel.Size()
	if h == height {
		return
	}
	b.panel.SetSize(panelWidth, height)
	if b.panel.IsVisible() {
		b.tray.reposition()
	}
}

// HidePanel 收起面板。
func (b *Bridge) HidePanel() { b.panel.Hide() }

// OpenDiscover 开「发现」窗（已开就拿到前面）。
func (b *Bridge) OpenDiscover() {
	b.panel.Hide()
	b.winMu.Lock()
	defer b.winMu.Unlock()
	if b.discover == nil {
		b.discover = b.newToolWindow("discover", "发现监控目标", "/discover.html", 380, 560)
	}
	b.discover.Show().Focus()
}

// OpenSettings 开设置窗。
func (b *Bridge) OpenSettings() {
	b.panel.Hide()
	b.winMu.Lock()
	defer b.winMu.Unlock()
	if b.settings == nil {
		b.settings = b.newToolWindow("settings", "Fluxwatch 设置", "/settings.html", 460, 560)
	}
	b.settings.Show().Focus()
}

func (b *Bridge) newToolWindow(name, title, url string, w, h int) *application.WebviewWindow {
	win := b.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             name,
		Title:            title,
		URL:              url,
		Width:            w,
		Height:           h,
		MinWidth:         w,
		MinHeight:        420,
		BackgroundType:   application.BackgroundTypeTranslucent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: application.MacWindow{
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
			InvisibleTitleBarHeight: 38,
		},
		Windows: application.WindowsWindow{
			BackdropType: application.Mica,
		},
		DefaultContextMenuDisabled: true,
	})
	win.Center()
	return win
}

// SettingsView 是设置窗要显示的全部内容。
type SettingsView struct {
	Settings     Settings   `json:"settings"`
	Path         string     `json:"path"`    // 设置文件路径（显示成 ~/.fluxwatch/settings.json）
	LoadErr      string     `json:"loadErr"` // 设置文件读坏了：照实显示
	Hook         HookStatus `json:"hook"`
	AutostartErr string     `json:"autostartErr"` // 开机启动状态读不到时的原因
	AutostartOn  bool       `json:"autostartOn"`  // 系统里实际登记的状态
	Engine       string     `json:"engine"`       // 「演示数据」或「引擎」
}

func (b *Bridge) GetSettings() SettingsView {
	v := SettingsView{Settings: b.store.get(), Path: "~/.fluxwatch/settings.json"}
	if runtime.GOOS == "windows" {
		v.Path = `%USERPROFILE%\.fluxwatch\settings.json`
	}
	if b.settingsErr != nil {
		v.LoadErr = b.settingsErr.Error()
	}
	if home, err := os.UserHomeDir(); err == nil {
		v.Hook = hookStatus(home)
	} else {
		v.Hook = HookStatus{Detail: "找不到用户目录"}
	}
	on, err := b.app.Autostart.IsEnabled()
	if err != nil {
		v.AutostartErr = err.Error()
	}
	v.AutostartOn = on
	v.Engine = "引擎"
	if b.fake {
		v.Engine = "演示数据（-fake）"
	}
	return v
}

// SaveSettings 校验、写文件；开机启动开关同步到系统登记。
func (b *Bridge) SaveSettings(s Settings) error {
	prev := b.store.get()
	if s.LaunchAtLogin != prev.LaunchAtLogin {
		var err error
		if s.LaunchAtLogin {
			err = b.app.Autostart.Enable()
		} else {
			err = b.app.Autostart.Disable()
		}
		if err != nil {
			return fmt.Errorf("开机启动没改成：%w", err)
		}
	}
	if err := b.store.save(s); err != nil {
		return err
	}
	b.settingsErr = nil
	applyVisibility(s.ShowMenuBar, s.ShowDock)
	return nil
}

// Quit 退出应用。
func (b *Bridge) Quit() { b.app.Quit() }
