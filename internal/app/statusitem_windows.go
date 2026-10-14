package app

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
)

// hotkeyLabel：Windows 上 ⌘ 对应 Ctrl，⌥ 对应 Alt（带 Win 键的组合系统保留）。
const hotkeyLabel = "Ctrl+Alt+F"

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procRegisterHK   = user32.NewProc("RegisterHotKey")
	procGetMessageW  = user32.NewProc("GetMessageW")
	procUnregisterHK = user32.NewProc("UnregisterHotKey")
)

const (
	modAlt      = 0x0001
	modControl  = 0x0002
	modNoRepeat = 0x4000
	vkF         = 0x46
	wmHotkey    = 0x0312
	hotkeyID    = 0xF1F1
)

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

// nativeSetup：Windows 只注册全局快捷键。RegisterHotKey(hWnd=0) 把 WM_HOTKEY 投到注册它的线程，
// 所以单起一个锁住系统线程的 goroutine 跑消息循环。托盘位置交给 Wails。
func nativeSetup(tc *trayController) (found bool, hotkeyErr error) {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		r, _, e := procRegisterHK.Call(0, hotkeyID, modControl|modAlt|modNoRepeat, vkF)
		if r == 0 {
			errc <- fmt.Errorf("RegisterHotKey：%v（多半是别的软件占了 %s）", e, hotkeyLabel)
			return
		}
		errc <- nil
		defer procUnregisterHK.Call(0, hotkeyID)
		var m winMsg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			if m.message == wmHotkey && m.wParam == hotkeyID {
				go tc.togglePanel()
			}
		}
	}()
	return true, <-errc
}

// placement：Windows 的托盘位置交给 Wails（图标在任务栏或溢出区都能贴），这里一律当作可贴、没有刘海。
func placement() trayPlacement { return trayPlacement{Found: true, Visible: true} }

// menuBarDark：Windows 任务栏图标按系统明暗各给一张，由 Wails 切；这里报读不到，调用方用系统外观。
func menuBarDark(_ *application.App) (dark, ok bool) { return false, false }

func centerPanelTop(w *application.WebviewWindow, offset int) {
	centerPanelTopGeneric(w, offset)
}

func activateApp() {}

func seedTrayPosition() {}

// 菜单栏 / Dock 图标的显隐只有 mac 有；Windows 的托盘图标固定显示。
func applyVisibility(menuBar, dock bool) {}
