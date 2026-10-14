package app

import (
	"os/exec"
	"strconv"
	"strings"
)

// nativeNotify 是 mac 上的退路：没打成 .app（go run、没有 bundle id）时 Wails 通知服务起不来，
// 用 osascript 的 display notification 发系统通知。
func nativeNotify(title, body string) error {
	script := "display notification " + strconv.Quote(body) + " with title " + strconv.Quote(title)
	return exec.Command("osascript", "-e", script).Run()
}

// reduceMotion 读「辅助功能 → 显示 → 减少动态效果」。读不到按没开处理（呼吸照常）。
func reduceMotion() bool {
	out, err := exec.Command("defaults", "read", "com.apple.universalaccess", "reduceMotion").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// iconFollowsMenuBar：mac 按当前菜单栏明暗画一张图（菜单栏外观变了重画），不用模板图标。
const iconFollowsMenuBar = true

// revealPath 在 Finder 里选中文件并带到前面（Wails 的 OpenFileManager 不会，看着像没反应）。
// macOS 14 起是「协作式激活」：不在前台的应用叫起来的 Finder 不会到前面，而面板本身不激活应用，
// 所以先趁用户这一下点击把自己激活，再 `open -R`。
func revealPath(path string) (bool, error) {
	activateApp()
	return true, exec.Command("open", "-R", path).Run()
}

// openDir 用 Finder 打开目录并带到前面。
func openDir(dir string) (bool, error) {
	activateApp()
	return true, exec.Command("open", dir).Run()
}

// trayIconSize：mac 菜单栏 22pt，按 2x 画 44px，Wails 会把图设成菜单栏厚度。
const trayIconSize = 44

// badgeInIcon：mac 的角标用菜单栏文字（SetLabel），不画进图标。
const badgeInIcon = false
