package app

/*
#cgo CFLAGS: -x objective-c -mmacosx-version-min=10.13
#cgo LDFLAGS: -framework Cocoa -framework Carbon
#include <stdlib.h>
#include "statusitem_darwin.h"
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// hotkeyLabel 是开关面板的全局快捷键，写进通知和菜单。
const hotkeyLabel = "⌥⌘F"

// statusAutosave 是状态项的 autosaveName：用户按住 ⌘ 拖过的位置，系统按这个名字记住（.app 有 bundle id 时才持久）。
const statusAutosave = "fluxwatch.status"

var (
	nativeMu   sync.Mutex
	nativeTray *trayController // 接 ObjC 回调（外观变化、快捷键）
)

//export fwGoMenuBarAppearance
func fwGoMenuBarAppearance(dark C.int) {
	nativeMu.Lock()
	tc := nativeTray
	nativeMu.Unlock()
	if tc != nil {
		go tc.setDark(dark == 1) // 回调在主线程上，换图要 InvokeSync，挪到别的 goroutine
	}
}

//export fwGoHotkey
func fwGoHotkey() {
	nativeMu.Lock()
	tc := nativeTray
	nativeMu.Unlock()
	if tc != nil {
		go tc.togglePanel()
	}
}

// nativeSetup 在应用起来之后调：找到状态项、设 autosaveName、盯菜单栏外观；注册全局快捷键。
// 状态项要等 Wails 在主线程上建好，找不到返回 false，调用方过一会儿重试。
func nativeSetup(tc *trayController) (found bool, hotkeyErr error) {
	nativeMu.Lock()
	nativeTray = tc
	nativeMu.Unlock()
	name := C.CString(statusAutosave)
	defer C.free(unsafe.Pointer(name))
	var st C.int
	application.InvokeSync(func() {
		found = C.fwStatusItemSetup(name) == 1
		st = C.fwHotkeyRegister()
	})
	if st != 0 {
		hotkeyErr = fmt.Errorf("RegisterEventHotKey 返回 %d（多半是别的软件占了 %s）", int(st), hotkeyLabel)
	}
	return found, hotkeyErr
}

// placement 读状态项此刻落在哪、在不在刘海里。
func placement() trayPlacement {
	var info C.fwItemInfo
	application.InvokeSync(func() { info = C.fwStatusItemInfo() })
	p := trayPlacement{
		Found: info.found == 1, Visible: info.visible == 1,
		X: float64(info.x), W: float64(info.w),
		ScreenX: float64(info.screenX), ScreenW: float64(info.screenW),
		HasNotch: info.hasNotch == 1, NotchL: float64(info.notchL), NotchR: float64(info.notchR),
	}
	return p
}

// menuBarDark 读菜单栏此刻的明暗（不是系统外观：浅色模式配深色壁纸时菜单栏也可能是暗的）。
func menuBarDark(_ *application.App) (dark, ok bool) {
	var v C.int
	application.InvokeSync(func() { v = C.fwMenuBarDark() })
	if v < 0 {
		return false, false
	}
	return v == 1, true
}

// centerPanelTop 把面板放到屏幕顶部正中（菜单栏下面）。
func centerPanelTop(w *application.WebviewWindow, offset int) {
	ptr := w.NativeWindow()
	if ptr == nil {
		return
	}
	application.InvokeSync(func() { C.fwCenterTop(ptr, C.int(offset)) })
}

// activateApp 让本应用成为前台（从快捷键打开面板时，不然面板拿不到键盘、点外面也不收起）。
func activateApp() {
	application.InvokeSync(func() { C.fwActivate() })
}
