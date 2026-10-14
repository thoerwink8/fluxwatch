package app

import (
	"syscall"
	"unsafe"
)

// Windows 的通知走 Wails 的通知服务（系统 toast）；它起不来时没有别的退路。
var nativeNotify func(title, body string) error

var procSystemParametersInfo = syscall.NewLazyDLL("user32.dll").NewProc("SystemParametersInfoW")

// reduceMotion 读 SPI_GETCLIENTAREAANIMATION：「显示动画」关掉时不呼吸。
func reduceMotion() bool {
	const spiGetClientAreaAnimation = 0x1042
	var on int32 = 1
	r, _, _ := procSystemParametersInfo.Call(spiGetClientAreaAnimation, 0, uintptr(unsafe.Pointer(&on)), 0)
	if r == 0 {
		return false
	}
	return on == 0
}

// Windows 任务栏没有模板图标：正常态按任务栏明暗各给一张中性色图标。
const useTemplateIcon = false

// trayIconSize：Windows 托盘图标 16px@100%，给 32px 让高 DPI 下也清楚。
const trayIconSize = 32

// badgeInIcon：Windows 托盘没有文字，角标画进图标。
const badgeInIcon = true
