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

// useTemplateIcon：mac 正常态用模板图标，系统按菜单栏明暗自动反色。
const useTemplateIcon = true

// trayIconSize：mac 菜单栏 22pt，按 2x 画 44px，Wails 会把图设成菜单栏厚度。
const trayIconSize = 44

// badgeInIcon：mac 的角标用菜单栏文字（SetLabel），不画进图标。
const badgeInIcon = false
