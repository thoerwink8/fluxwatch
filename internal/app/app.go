// Package app 是 Fluxwatch 的托盘界面：托盘图标、贴托盘的面板、发现窗、设置窗、通知。
// 数据只从 model.Provider 拿；界面是 frontend/ 下的纯 HTML/CSS/JS，经 Wails v3 的绑定和事件取数。
package app

import (
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// Options 是启动参数。
type Options struct {
	Provider model.Provider
	Assets   fs.FS  // frontend/ 的内容
	Fake     bool   // 用的是演示数据：界面上标出来
	Version  string // 发版注入的版本号
	ShotsDir string // 截图模式：拍完退出（要 -tags shots 构建）
}

const (
	panelWidth   = 330
	panelMinH    = 160
	panelMaxH    = 720
	eventSnap    = "fluxwatch:snapshot"
	eventTheme   = "fluxwatch:theme"
	eventOpenRow = "fluxwatch:panel-shown"
)

// Run 起托盘应用，阻塞到退出。
func Run(o Options) error {
	if o.Provider == nil {
		return fmt.Errorf("没有 Provider")
	}
	if o.ShotsDir != "" && !shotsAvailable {
		return fmt.Errorf("截图模式要在 mac 上用 -tags shots 构建")
	}
	dir, err := fluxwatchDir()
	if err != nil {
		return fmt.Errorf("找不到用户目录：%w", err)
	}
	store := newSettingsStore(filepath.Join(dir, "settings.json"))
	settingsErr := store.load()
	if settingsErr != nil {
		log.Printf("设置：%v（先用默认值，不覆盖原文件）", settingsErr)
	}

	n := newNotifier()
	b := &Bridge{p: o.Provider, store: store, settingsErr: settingsErr, hist: newHistory(), fake: o.Fake, version: o.Version}

	app := application.New(application.Options{
		Name:        "Fluxwatch",
		Description: "盯 AI 流量，卡住时指出卡在哪一层",
		Services: []application.Service{
			application.NewService(b),
			application.NewService(n),
		},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(o.Assets)},
		Mac: application.MacOptions{
			// 辅助应用：只在菜单栏，不进 Dock、不抢 Cmd-Tab
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
		Windows: application.WindowsOptions{},
	})
	b.app = app

	panel := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "panel",
		Title:            "Fluxwatch",
		URL:              "/",
		Width:            panelWidth,
		Height:           520,
		Frameless:        true,
		AlwaysOnTop:      true,
		Hidden:           true,
		DisableResize:    true,
		HideOnEscape:     true,
		HideOnFocusLost:  true,
		BackgroundType:   application.BackgroundTypeTranslucent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: application.MacWindow{
			Backdrop:     application.MacBackdropTranslucent,
			CornerRadius: 12,
			WindowLevel:  application.MacWindowLevelPopUpMenu,
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces |
				application.MacWindowCollectionBehaviorFullScreenAuxiliary,
		},
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
			BackdropType:    application.Acrylic,
		},
		DefaultContextMenuDisabled: true,
	})
	// 关窗 = 收起，不退出
	panel.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		panel.Hide()
		e.Cancel()
	})
	panel.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		panel.EmitEvent(eventOpenRow)
	})
	b.panel = panel

	menu := application.NewMenu()
	menu.Add("显示面板").OnClick(func(*application.Context) { b.tray.showPanel() })
	menu.Add("发现监控目标…").OnClick(func(*application.Context) { b.OpenDiscover() })
	menu.Add("设置…").OnClick(func(*application.Context) { b.OpenSettings() })
	menu.AddSeparator()
	menu.Add("退出 Fluxwatch").OnClick(func(*application.Context) { app.Quit() })

	b.tray = newTrayController(app, panel, menu)
	b.tray.start(false, o.Provider.Snapshot())

	// 外观变化：换图标底色，通知所有窗口刷新主题（WebView 的 prefers-color-scheme 也会变，这里兜一下）
	app.Event.OnApplicationEvent(events.Common.ThemeChanged, func(*application.ApplicationEvent) {
		dark := app.Env.IsDarkMode()
		b.tray.setDark(dark)
		app.Event.Emit(eventTheme, dark)
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		b.tray.setDark(app.Env.IsDarkMode())
		go b.pump(n)
		if o.ShotsDir != "" {
			go b.runShots(o.ShotsDir)
		}
	})

	return app.Run()
}

// pump 订阅 Provider：每秒换托盘图标、攒示波器历史、推给面板、按跳变发通知。
func (b *Bridge) pump(n *notifier) {
	ch, cancel := b.p.Subscribe()
	defer cancel()
	tr := newTransitions()
	handle := func(s model.Snapshot) {
		b.hist.merge(s)
		b.tray.update(s)
		b.app.Event.Emit(eventSnap, lite(s))
		alerts := tr.next(s)
		if b.store.get().Notify {
			for _, a := range alerts {
				title, body := a.message()
				go n.send(title, body)
			}
		}
	}
	handle(b.p.Snapshot())
	rm := time.NewTicker(30 * time.Second)
	defer rm.Stop()
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				return
			}
			handle(s)
		case <-rm.C:
			b.tray.refreshReduceMotion()
		case <-b.app.Context().Done():
			return
		}
	}
}

// lite 去掉 15 分钟轨迹再推给前端（每秒一份，轨迹按需用 Bridge.Trace 取）。
func lite(s model.Snapshot) model.Snapshot {
	out := s
	out.Targets = make([]model.Target, len(s.Targets))
	for i, t := range s.Targets {
		t.Trace = nil
		out.Targets[i] = t
	}
	return out
}
