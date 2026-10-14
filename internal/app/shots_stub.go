//go:build !shots || !darwin

package app

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const shotsAvailable = false

func snapshotWindow(*application.WebviewWindow, string) error {
	return errors.New("截图模式要在 mac 上用 -tags shots 构建")
}
