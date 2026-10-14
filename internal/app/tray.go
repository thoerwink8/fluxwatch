package app

import (
	"fmt"
	"image/color"
	"log"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

const breathFrames = 19 // 1.9s 一个周期，每 100ms 一帧

// iconSet 是一套预先画好的图标（某个尺寸、某种底色）。
type iconSet struct {
	still  map[iconKind][]byte // 每态静止图
	breath [][]byte            // 卡住态呼吸帧
	badged map[[3]int][]byte   // Windows：带角标的图，key = {kind, 角标数, 呼吸帧}，按需画、缓存
}

func buildIconSet(dark bool, size int) *iconSet {
	pal := paletteLight
	if dark {
		pal = paletteDark
	}
	set := &iconSet{still: map[iconKind][]byte{}, badged: map[[3]int][]byte{}}
	for k, c := range pal {
		set.still[k] = renderIcon(iconSpec{size: size, ink: inkFor(k, dark, c), alpha: 1, question: k == iconUnknown})
	}
	for i := 0; i < breathFrames; i++ {
		set.breath = append(set.breath, renderIcon(iconSpec{size: size, ink: pal[iconCrit], alpha: breathAlpha(i, breathFrames)}))
	}
	return set
}

// inkFor：正常态不用模板图标，自己按底色画单色字形（亮底深色、暗底浅色），像系统自带的菜单栏图标；其他态用语义色。
func inkFor(k iconKind, dark bool, c color.NRGBA) color.NRGBA {
	if k != iconGood {
		return c
	}
	if dark {
		return neutralOnDark
	}
	return neutralOnLight
}

// trayPlacement 是状态项此刻的位置（mac；其他平台恒为「可贴」）。
type trayPlacement struct {
	Found, Visible   bool
	X, W             float64 // 状态项左边和宽度（屏幕坐标，点）
	ScreenX, ScreenW float64
	HasNotch         bool
	NotchL, NotchR   float64 // 刘海左右边
}

// InNotch：状态项和刘海有重叠（哪怕只被挡住一部分，也基本点不到了）。
func (p trayPlacement) InNotch() bool {
	return p.Found && p.HasNotch && p.X+p.W > p.NotchL && p.X < p.NotchR
}

// usable：图标看得见、点得到，面板可以贴着它。
func (p trayPlacement) usable() bool { return p.Found && p.Visible && !p.InNotch() }

func (p trayPlacement) String() string {
	if !p.Found {
		return "没找到状态项"
	}
	s := fmt.Sprintf("x=%.0f w=%.0f 屏宽 %.0f", p.X, p.W, p.ScreenW)
	if p.HasNotch {
		s += fmt.Sprintf(" 刘海 %.0f–%.0f", p.NotchL, p.NotchR)
	}
	if p.InNotch() {
		s += " 落在刘海里"
	}
	return s
}

// notchMessage 是图标被刘海挡住时那条通知（只发一次）。
func notchMessage() (title, body string) {
	return "Fluxwatch", "菜单栏太满，Fluxwatch 图标被刘海挡住了：按 " + hotkeyLabel + " 打开面板，或按住 ⌘ 把它拖到右边"
}

// trayController 管托盘图标、角标、呼吸和贴托盘的面板。
type trayController struct {
	app    *application.App
	panel  *application.WebviewWindow
	menu   *application.Menu
	offset int

	mu      sync.Mutex
	tray    *application.SystemTray
	kind    iconKind
	badge   int
	dark    bool
	reduce  bool
	frame   int
	sets    map[bool]*iconSet // key: 暗色底
	stopAni chan struct{}

	placeMu     sync.Mutex
	lastPlace   string
	notchWarned bool
}

func newTrayController(app *application.App, panel *application.WebviewWindow, menu *application.Menu) *trayController {
	tc := &trayController{
		app: app, panel: panel, menu: menu, offset: 6,
		kind: -1, sets: map[bool]*iconSet{},
		reduce: reduceMotion(),
	}
	tc.sets[false] = buildIconSet(false, trayIconSize)
	tc.sets[true] = buildIconSet(true, trayIconSize)
	return tc
}

// start 在 app.Run 之前调用：按第一份快照建托盘。整个运行期只建这一只，换图不重建
// （重建会把图标甩到菜单栏最左、更容易落进刘海）。
func (tc *trayController) start(dark bool, s model.Snapshot) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.dark = dark
	t := tc.app.SystemTray.New()
	t.SetTooltip("Fluxwatch")
	t.SetMenu(tc.menu)
	t.AttachWindow(tc.panel).WindowOffset(tc.offset).WindowDebounce(300 * time.Millisecond)
	tc.tray = t
	worst, badge := trayView(s)
	tc.kind = kindFor(worst, anyBusy(s))
	tc.badge = badge
	tc.applyLocked()
}

func anyBusy(s model.Snapshot) bool {
	for _, t := range s.Targets {
		if t.State == model.StateBusy {
			return true
		}
	}
	return false
}

// update 按快照换图标和角标。
func (tc *trayController) update(s model.Snapshot) {
	worst, badge := trayView(s)
	k := kindFor(worst, anyBusy(s))
	tc.mu.Lock()
	defer tc.mu.Unlock()
	changed := k != tc.kind || badge != tc.badge
	tc.kind, tc.badge = k, badge
	if changed {
		tc.applyLocked()
	}
	tc.tray.SetTooltip(tooltipFor(s))
}

// setDark 在菜单栏（mac）/ 系统（Windows）明暗变化时调用：换一套底色调过的图标，不重建托盘。
func (tc *trayController) setDark(dark bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if dark == tc.dark {
		return
	}
	tc.dark = dark
	log.Printf("托盘：底色变成%s，重画图标", map[bool]string{true: "暗", false: "亮"}[dark])
	tc.applyLocked()
}

// refreshAppearance 重读底色：mac 读菜单栏本身的外观（浅色模式配深色壁纸时菜单栏也是暗的），读不到用系统外观。
func (tc *trayController) refreshAppearance() {
	dark, ok := menuBarDark(tc.app)
	if !ok {
		dark = tc.app.Env.IsDarkMode()
	}
	tc.setDark(dark)
}

// applyLocked 把 kind / badge 画到托盘上。
// 不用模板图标：Wails v3 beta.29 一旦调过 SetTemplateIcon，这只托盘之后的图都被当成模板（彩色变单色），
// 以前的绕法是销毁重建托盘，每次都把图标甩到菜单栏最左。现在正常态也自己按底色画，始终 SetIcon。
func (tc *trayController) applyLocked() {
	if iconFollowsMenuBar {
		// mac：按当前菜单栏明暗画好的一张
		tc.tray.SetIcon(tc.iconFor(tc.dark, tc.frame))
	} else {
		// Windows：按任务栏明暗各给一张，系统主题变了 Wails 自己切
		tc.tray.SetIcon(tc.iconFor(false, tc.frame))
		tc.tray.SetDarkModeIcon(tc.iconFor(true, tc.frame))
	}
	if !badgeInIcon && tc.badge > 0 {
		tc.tray.SetLabel(fmt.Sprintf("%d", tc.badge))
	} else {
		tc.tray.SetLabel("")
	}
	tc.syncAnimationLocked()
}

// iconFor 取当前态的图：卡住且没开减少动效时取呼吸帧；Windows 有角标时把角标画进去（按需画、缓存）。
func (tc *trayController) iconFor(dark bool, frame int) []byte {
	set := tc.sets[dark]
	breathing := tc.kind == iconCrit && !tc.reduce
	fi := 0
	if breathing {
		fi = frame % breathFrames
	}
	if badgeInIcon && tc.badge > 0 {
		key := [3]int{int(tc.kind), tc.badge, fi}
		if b, ok := set.badged[key]; ok {
			return b
		}
		pal := paletteLight
		ink := neutralOnLight
		if dark {
			pal, ink = paletteDark, neutralOnDark
		}
		if tc.kind != iconGood {
			ink = pal[tc.kind]
		}
		alpha := 1.0
		if breathing {
			alpha = breathAlpha(fi, breathFrames)
		}
		b := renderIcon(iconSpec{size: trayIconSize, ink: ink, alpha: alpha, badge: tc.badge, badgeColor: pal[iconCrit], question: tc.kind == iconUnknown})
		set.badged[key] = b
		return b
	}
	if breathing {
		return set.breath[fi]
	}
	return set.still[tc.kind]
}

// syncAnimationLocked：卡住且没开「减少动态效果」时起呼吸，否则停。
func (tc *trayController) syncAnimationLocked() {
	want := tc.kind == iconCrit && !tc.reduce
	if want && tc.stopAni == nil {
		stop := make(chan struct{})
		tc.stopAni = stop
		go tc.animate(stop)
	}
	if !want && tc.stopAni != nil {
		close(tc.stopAni)
		tc.stopAni = nil
	}
}

func (tc *trayController) animate(stop chan struct{}) {
	tk := time.NewTicker(100 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			tc.mu.Lock()
			tc.frame++
			if iconFollowsMenuBar {
				tc.tray.SetIcon(tc.iconFor(tc.dark, tc.frame))
			} else {
				tc.tray.SetIcon(tc.iconFor(false, tc.frame))
				tc.tray.SetDarkModeIcon(tc.iconFor(true, tc.frame))
			}
			tc.mu.Unlock()
		}
	}
}

// refreshReduceMotion 每隔一阵重读系统设置（mac 上改了「减少动态效果」不发事件）。
func (tc *trayController) refreshReduceMotion() {
	r := reduceMotion()
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if r != tc.reduce {
		tc.reduce = r
		tc.applyLocked()
	}
}

// showPanel 把面板贴到托盘下面显示；图标看不见（落进刘海、没找到）时没处可贴，放到屏幕顶部正中。
func (tc *trayController) showPanel() {
	tc.mu.Lock()
	t := tc.tray
	tc.mu.Unlock()
	if tc.checkPlacement().usable() {
		t.ShowWindow()
		return
	}
	centerPanelTop(tc.panel, tc.offset)
	tc.panel.Show().Focus()
}

// togglePanel 是全局快捷键的动作：开着就收起，收着就打开。
func (tc *trayController) togglePanel() {
	if tc.panel.IsVisible() {
		tc.panel.Hide()
		return
	}
	activateApp()
	tc.showPanel()
}

// reposition 面板高度变了之后重新放一次（窗口改尺寸后不会自己跟着托盘走）。
func (tc *trayController) reposition() {
	tc.mu.Lock()
	t := tc.tray
	tc.mu.Unlock()
	if tc.checkPlacement().usable() {
		_ = t.PositionWindow(tc.panel, tc.offset)
		return
	}
	centerPanelTop(tc.panel, tc.offset)
}

// setupNative 在应用起来之后调：找状态项（设 autosaveName、盯菜单栏外观）、注册全局快捷键、读一次底色。
// 状态项由 Wails 在主线程上异步建，找不到就隔一会儿再找。
func (tc *trayController) setupNative() {
	for i := 0; i < 20; i++ {
		found, herr := nativeSetup(tc)
		if herr != nil {
			log.Printf("全局快捷键 %s 注册失败：%v", hotkeyLabel, herr)
		}
		if found {
			break
		}
		if i == 19 {
			log.Printf("托盘：找不到状态项，菜单栏外观和刘海检查不可用")
		}
		time.Sleep(250 * time.Millisecond)
	}
	tc.refreshAppearance()
}

// checkPlacement 读一次状态项的位置；位置变了记一行日志。
func (tc *trayController) checkPlacement() trayPlacement {
	p := placement()
	tc.placeMu.Lock()
	if s := p.String(); s != tc.lastPlace {
		tc.lastPlace = s
		log.Printf("托盘：状态项 %s", s)
	}
	tc.placeMu.Unlock()
	return p
}

// warnIfNotched：状态项落进刘海时发一次通知（每次启动最多一次），返回这次有没有发。
func (tc *trayController) warnIfNotched(send func(title, body string)) bool {
	p := tc.checkPlacement()
	tc.placeMu.Lock()
	defer tc.placeMu.Unlock()
	if !p.InNotch() || tc.notchWarned {
		return false
	}
	tc.notchWarned = true
	go send(notchMessage())
	return true
}

func tooltipFor(s model.Snapshot) string {
	worst, badge := trayView(s)
	switch {
	case badge > 0:
		return fmt.Sprintf("Fluxwatch · %d 个目标在报警", badge)
	case worst == model.StateUnknown:
		return "Fluxwatch · 读不到数据（点开看原因）"
	}
	return "Fluxwatch · 没有目标在报警"
}

// centerPanelTopGeneric 用 Wails 的屏幕信息把面板放到屏幕顶部正中（Windows 等没有原生实现的平台）。
func centerPanelTopGeneric(w *application.WebviewWindow, offset int) {
	scr, err := w.GetScreen()
	if err != nil || scr == nil {
		w.Center()
		return
	}
	pw, _ := w.Size()
	wa := scr.WorkArea
	w.SetPosition(wa.X+(wa.Width-pw)/2, wa.Y+offset)
}
