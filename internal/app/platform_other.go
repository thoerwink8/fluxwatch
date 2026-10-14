//go:build !darwin && !windows

package app

// 只做 mac 和 Windows；其他平台给能编译的空实现。
var nativeNotify func(title, body string) error

func reduceMotion() bool { return false }

const iconFollowsMenuBar = false

// 其他平台交给 Wails 的 OpenFileManager / OpenFile。
func revealPath(string) (bool, error) { return false, nil }
func openDir(string) (bool, error)    { return false, nil }

const trayIconSize = 32

const badgeInIcon = true
