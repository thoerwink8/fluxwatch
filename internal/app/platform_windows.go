package app

import (
	"errors"
	"os/exec"
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

// iconFollowsMenuBar：Windows 正常态按任务栏明暗各给一张中性色图标，由 Wails 随系统主题切。
const iconFollowsMenuBar = false

// revealPath 在资源管理器里选中文件。explorer 的 /select, 后面要紧跟带引号的路径，所以自己拼命令行；
// explorer 成功时也常返回 1，退出码不可信，只看能不能起来。
func revealPath(path string) (bool, error) {
	return true, startExplorer(`explorer.exe /select,"` + path + `"`)
}

// openDir 用资源管理器打开目录。
func openDir(dir string) (bool, error) {
	return true, startExplorer(`explorer.exe "` + dir + `"`)
}

func startExplorer(cmdline string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdline}
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil
	}
	return err
}

// trayIconSize：Windows 托盘图标 16px@100%，给 32px 让高 DPI 下也清楚。
const trayIconSize = 32

// badgeInIcon：Windows 托盘没有文字，角标画进图标。
const badgeInIcon = true
