//go:build !darwin && !windows

package app

// 只做 mac 和 Windows；其他平台给能编译的空实现。
var nativeNotify func(title, body string) error

func reduceMotion() bool { return false }

const useTemplateIcon = false

const trayIconSize = 32

const badgeInIcon = true
