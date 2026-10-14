package app

import (
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

const breathFrames = 19 // 1.9s 一个周期，每 100ms 一帧

// iconSet 是一套预先画好的图标（某个尺寸、某种底色）。
type iconSet struct {
	template []byte              // mac 正常态模板图标
	still    map[iconKind][]byte // 每态静止图
	breath   [][]byte            // 卡住态呼吸帧
	badged   map[[3]int][]byte   // Windows：带角标的图，key = {kind, 角标数, 呼吸帧}，按需画、缓存
}

func buildIconSet(dark bool, size int) *iconSet {
	pal := paletteLight
	if dark {
		pal = paletteDark
	}
	set := &iconSet{still: map[iconKind][]byte{}, badged: map[[3]int][]byte{}}
	set.template = renderIcon(iconSpec{size: size, ink: templateInk, alpha: 1})
	for k, c := range pal {
		ink := c
		if k == iconGood && !useTemplateIcon {
			// 没有模板图标的平台：正常态用中性字形色，像系统自带的托盘图标
			ink = neutralOnLight
			if dark {
				ink = neutralOnDark
			}
		}
		set.still[k] = renderIcon(iconSpec{size: size, ink: ink, alpha: 1})
	}
	for i := 0; i < breathFrames; i++ {
		set.breath = append(set.breath, renderIcon(iconSpec{size: size, ink: pal[iconCrit], alpha: breathAlpha(i, breathFrames)}))
	}
	return set
}

// trayController 管托盘图标、角标、呼吸和贴托盘的面板。
type trayController struct {
	app    *application.App
	panel  *application.WebviewWindow
	menu   *application.Menu
	offset int

	mu      sync.Mutex
	tray    *application.SystemTray
	isTmpl  bool // 当前这只托盘用过模板图标
	kind    iconKind
	badge   int
	dark    bool
	reduce  bool
	frame   int
	sets    map[bool]*iconSet // key: 暗色底
	stopAni chan struct{}
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

// start 在 app.Run 之前调用：按第一份快照建托盘。起步就是报警态时直接挂彩色图标，
// 免得「先模板、再彩色」触发下面 applyLocked 里的重建。
func (tc *trayController) start(dark bool, s model.Snapshot) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.dark = dark
	tc.tray = tc.newTray()
	tc.kind = kindFor(s.Worst, anyBusy(s))
	tc.badge = s.Alerting
	tc.applyLocked(true)
}

func anyBusy(s model.Snapshot) bool {
	for _, t := range s.Targets {
		if t.State == model.StateBusy {
			return true
		}
	}
	return false
}

func (tc *trayController) newTray() *application.SystemTray {
	t := tc.app.SystemTray.New()
	t.SetTooltip("Fluxwatch")
	t.SetMenu(tc.menu)
	t.AttachWindow(tc.panel).WindowOffset(tc.offset).WindowDebounce(300 * time.Millisecond)
	tc.isTmpl = false
	return t
}

// update 按快照换图标和角标。
func (tc *trayController) update(s model.Snapshot) {
	k := kindFor(s.Worst, anyBusy(s))
	tc.mu.Lock()
	defer tc.mu.Unlock()
	changed := k != tc.kind || s.Alerting != tc.badge
	tc.kind, tc.badge = k, s.Alerting
	if changed {
		tc.applyLocked(false)
	}
	tc.tray.SetTooltip(tooltipFor(s))
}

// setDark 在系统外观变化时调用：换一套底色调过的图标。
func (tc *trayController) setDark(dark bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if dark == tc.dark {
		return
	}
	tc.dark = dark
	tc.applyLocked(false)
}

// applyLocked 把 kind / badge 画到托盘上。
//
// Wails v3 beta.29 的坑：mac 上一旦调过 SetTemplateIcon，这只托盘之后 SetIcon 的图也一直被当成模板
// （macosSystemTray.isTemplateIcon 只置真不复位），彩色图标会变成单色。绕法：从模板图标切到彩色时，
// 销毁旧托盘、新建一只（只在「正常 → 报警」那一下发生，平时不闪）。
func (tc *trayController) applyLocked(initial bool) {
	wantTmpl := useTemplateIcon && tc.kind == iconGood
	if !wantTmpl && tc.isTmpl && !initial {
		tc.tray.Destroy()
		tc.tray = tc.newTray()
	}
	switch {
	case wantTmpl:
		tc.tray.SetTemplateIcon(tc.sets[tc.dark].template)
		tc.isTmpl = true
	case !useTemplateIcon:
		// Windows：按任务栏明暗各给一张，系统主题变了 Wails 自己切
		tc.tray.SetIcon(tc.iconFor(false, tc.frame))
		tc.tray.SetDarkModeIcon(tc.iconFor(true, tc.frame))
	default:
		tc.tray.SetIcon(tc.iconFor(tc.dark, tc.frame))
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
		b := renderIcon(iconSpec{size: trayIconSize, ink: ink, alpha: alpha, badge: tc.badge, badgeColor: pal[iconCrit]})
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
			if useTemplateIcon {
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
		tc.applyLocked(false)
	}
}

// showPanel 把面板贴到托盘下面显示。
func (tc *trayController) showPanel() {
	tc.mu.Lock()
	t := tc.tray
	tc.mu.Unlock()
	t.ShowWindow()
}

// reposition 面板高度变了之后重新贴一次（窗口改尺寸后不会自己跟着托盘走）。
func (tc *trayController) reposition() {
	tc.mu.Lock()
	t := tc.tray
	tc.mu.Unlock()
	_ = t.PositionWindow(tc.panel, tc.offset)
}

func tooltipFor(s model.Snapshot) string {
	if s.Alerting == 0 {
		return "Fluxwatch · 没有目标在报警"
	}
	return fmt.Sprintf("Fluxwatch · %d 个目标在报警", s.Alerting)
}
