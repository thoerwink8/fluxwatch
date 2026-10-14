package app

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 截图模式（`-shots <目录>`，要在 mac 上用 -tags shots 构建）：真起应用、用演示数据，
// 弹出贴托盘的面板、发现窗、设置窗各拍一张，再把托盘图标四态画成一条，然后退出。
// 这台机器若没有「屏幕录制」权限，系统截屏拍不了，所以拍的是 WKWebView 自己渲染出的网页内容，
// 再垫到草图里的舞台底色上；窗口后面的系统毛玻璃不在图里。

func (b *Bridge) runShots(dir string) {
	defer b.app.Quit()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("截图：%v", err)
		return
	}
	dark := b.app.Env.IsDarkMode()
	theme := "light"
	if dark {
		theme = "dark"
	}
	shoot := func(name string, w *application.WebviewWindow) {
		take := func(p string) error { return snapshotWindow(w, p) }
		raw := filepath.Join(os.TempDir(), "fluxwatch-shot-"+name+".png")
		if err := take(raw); err != nil {
			log.Printf("截图 %s：%v", name, err)
			return
		}
		out := filepath.Join(dir, fmt.Sprintf("%s-%s.png", name, theme))
		if err := composeOnStage(raw, out, dark); err != nil {
			log.Printf("截图 %s：%v", name, err)
			return
		}
		_ = os.Remove(raw)
		log.Printf("截图：写了 %s", out)
	}

	time.Sleep(3 * time.Second) // 等几份快照，示波器有东西
	b.tray.showPanel()
	time.Sleep(2 * time.Second)
	px, py := b.panel.Position()
	pw, ph := b.panel.Size()
	log.Printf("截图：面板贴在 (%d,%d) 尺寸 %dx%d，可见=%v", px, py, pw, ph, b.panel.IsVisible())
	shoot("panel", b.panel)

	b.OpenDiscover()
	time.Sleep(2500 * time.Millisecond)
	b.app.Event.Emit("fluxwatch:shots-select")
	time.Sleep(800 * time.Millisecond)
	shoot("discover", b.discover)

	b.OpenSettings()
	time.Sleep(2 * time.Second)
	shoot("settings", b.settings)

	if err := writeTrayStrip(filepath.Join(dir, "tray-"+theme+".png"), dark); err != nil {
		log.Printf("截图 tray：%v", err)
	}
}

// composeOnStage 把网页截图垫到草图 --stage-light / --stage 的底色上，四周留白。
func composeOnStage(in, out string, dark bool) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	pad := 48
	sb := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, sb.Dx()+2*pad, sb.Dy()+2*pad))
	c0, c1 := color.NRGBA{0xE9, 0xEE, 0xF5, 0xFF}, color.NRGBA{0xEE, 0xEA, 0xF2, 0xFF}
	if dark {
		c0, c1 = color.NRGBA{0x14, 0x19, 0x22, 0xFF}, color.NRGBA{0x18, 0x14, 0x22, 0xFF}
	}
	W, H := dst.Bounds().Dx(), dst.Bounds().Dy()
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			t := (float64(x)/float64(W) + float64(y)/float64(H)) / 2
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(float64(c0.R)*(1-t) + float64(c1.R)*t),
				G: uint8(float64(c0.G)*(1-t) + float64(c1.G)*t),
				B: uint8(float64(c0.B)*(1-t) + float64(c1.B)*t), A: 0xFF,
			})
		}
	}
	draw.Draw(dst, sb.Add(image.Pt(pad, pad)).Sub(sb.Min), src, sb.Min, draw.Over)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

// writeTrayStrip 把托盘图标五态 + 角标画在一条仿菜单栏上（正常态按菜单栏明暗画单色，和真菜单栏一致）。
func writeTrayStrip(out string, dark bool) error {
	const icon, gap, padX, h = 44, 28, 24, 52
	bg := color.NRGBA{0xF2, 0xF2, 0xF4, 0xFF}
	fg := color.NRGBA{0x1D, 0x1D, 0x1F, 0xFF}
	pal := paletteLight
	if dark {
		bg, fg, pal = color.NRGBA{0x2A, 0x2A, 0x2E, 0xFF}, color.NRGBA{0xF5, 0xF5, 0xF7, 0xFF}, paletteDark
	}
	items := [][]byte{
		renderIcon(iconSpec{size: icon, ink: fg, alpha: 1}),                                                    // 正常（按菜单栏明暗画的单色）
		renderIcon(iconSpec{size: icon, ink: pal[iconWarn], alpha: 1}),                                         // 可疑
		renderIcon(iconSpec{size: icon, ink: pal[iconCrit], alpha: 1}),                                         // 卡住（亮帧）
		renderIcon(iconSpec{size: icon, ink: pal[iconCrit], alpha: breathAlpha(breathFrames/2, breathFrames)}), // 卡住（暗帧）
		renderIcon(iconSpec{size: icon, ink: pal[iconBusy], alpha: 1}),                                         // 本地忙
		renderIcon(iconSpec{size: icon, ink: pal[iconUnknown], alpha: 1, question: true}),                      // 读不到（灰色问号）
		renderIcon(iconSpec{size: 32, ink: pal[iconCrit], alpha: 1, badge: 2, badgeColor: pal[iconCrit]}),      // Windows：角标画进图标
	}
	W := padX*2 + len(items)*icon + (len(items)-1)*gap
	dst := image.NewNRGBA(image.Rect(0, 0, W, h))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	x := padX
	for _, b := range items {
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return err
		}
		ib := img.Bounds()
		off := image.Pt(x+(icon-ib.Dx())/2, (h-ib.Dy())/2)
		draw.Draw(dst, ib.Add(off), img, ib.Min, draw.Over)
		x += icon + gap
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}
