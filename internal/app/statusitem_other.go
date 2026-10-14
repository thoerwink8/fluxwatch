//go:build !darwin && !windows

package app

import "github.com/wailsapp/wails/v3/pkg/application"

const hotkeyLabel = "Ctrl+Alt+F"

// 只做 mac 和 Windows；其他平台不注册快捷键。
func nativeSetup(*trayController) (bool, error) { return true, nil }

func placement() trayPlacement { return trayPlacement{Found: true, Visible: true} }

func menuBarDark(*application.App) (bool, bool) { return false, false }

func centerPanelTop(w *application.WebviewWindow, offset int) { centerPanelTopGeneric(w, offset) }

func activateApp() {}
