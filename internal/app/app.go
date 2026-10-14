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
	eventHidden  = "fluxwatch:panel-hidden"
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
	b := &Bridge{p: o.Provider, store: store, settingsErr: settingsErr, hist: newHistory(), fake: o.Fake, version: o.Version,
		state: newStateWriter(filepath.Join(dir, stateFile))}

	seedTrayPosition()
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
	// 面板关着不推快照（省 webview 每秒重画）：可见时每秒推，一打开先补推一份最新的
	panel.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		b.panelShown.Store(true)
		panel.EmitEvent(eventSnap, lite(b.p.Snapshot()))
		panel.EmitEvent(eventOpenRow)
	})
	panel.OnWindowEvent(events.Common.WindowHide, func(*application.WindowEvent) {
		b.panelShown.Store(false)
		panel.EmitEvent(eventHidden)
	})
	b.panel = panel

	menu := application.NewMenu()
	menu.Add("显示面板（" + hotkeyLabel + "）").OnClick(func(*application.Context) { b.tray.showPanel() })
	menu.Add("发现监控目标…").OnClick(func(*application.Context) { b.OpenDiscover() })
	menu.Add("设置…").OnClick(func(*application.Context) { b.OpenSettings() })
	menu.AddSeparator()
	menu.Add("退出 Fluxwatch").OnClick(func(*application.Context) { app.Quit() })

	b.tray = newTrayController(app, panel, menu)
	b.tray.applyVis = func() {
		s := b.store.get()
		applyVisibility(s.ShowMenuBar, s.ShowDock)
	}
	// 点 Dock 图标：打开面板（Wails 自带的那个只会把隐藏的窗口全弹出来）
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		b.tray.showPanel()
	})
	b.tray.start(false, o.Provider.Snapshot())

	// 外观变化：重读图标底色（mac 读菜单栏本身的明暗，状态项按钮的外观变化另有回调），
	// 通知所有窗口刷新主题（WebView 的 prefers-color-scheme 也会变，这里兜一下）
	app.Event.OnApplicationEvent(events.Common.ThemeChanged, func(*application.ApplicationEvent) {
		go b.tray.refreshAppearance()
		app.Event.Emit(eventTheme, app.Env.IsDarkMode())
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			b.tray.setupNative()
			// 等菜单栏排好再看图标落在哪；落进刘海就提示一次怎么打开面板
			time.Sleep(2 * time.Second)
			b.tray.warnIfNotched(n.send)
		}()
		go b.pump(n)
		if o.ShotsDir != "" {
			go b.runShots(o.ShotsDir)
		}
	})

	return app.Run()
}

// pump 订阅 Provider：每秒换托盘图标、攒示波器历史、写 state.json、面板开着时推给面板、按跳变发通知。
func (b *Bridge) pump(n *notifier) {
	ch, cancel := b.p.Subscribe()
	defer cancel()
	tr := newTransitions()
	handle := func(s model.Snapshot) {
		b.hist.merge(s)
		b.tray.update(s)
		ls := lite(s)
		b.state.write(ls)
		if b.panelShown.Load() {
			b.panel.EmitEvent(eventSnap, ls)
		}
		alerts := tr.next(s)
		notify := b.store.get().Notify
		notched := false
		if !notify && hasStall(alerts) {
			notched = b.tray.checkPlacement().InNotch() // 只在要决定发不发时才读位置
		}
		for _, a := range alerts {
			if shouldNotify(a, notify, notched) {
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
			b.tray.refreshAppearance()
			b.tray.warnIfNotched(n.send) // 别的软件后来挤进菜单栏，也可能把图标挤进刘海
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
