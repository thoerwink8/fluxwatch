//go:build ignore

// 生成 scripts/assets/Fluxwatch.icns：深色圆角底 + 三条高低柱（和托盘图标同一个轮廓）。
// 用法（仅 mac，要 iconutil）：go run scripts/mkicon.go
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// 圆角矩形的有符号距离，<0 在内部。
func sdRoundRect(x, y, x0, y0, x1, y1, r float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	dx, dy := math.Abs(x-cx)-hx, math.Abs(y-cy)-hy
	return math.Hypot(math.Max(dx, 0), math.Max(dy, 0)) + math.Min(math.Max(dx, dy), 0) - r
}

func cover(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d)) }

func render(size int) *image.NRGBA {
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	// macOS 图标：824/1024 的圆角方块居中
	m := s * 100 / 1024
	bg0, bg1 := m, s-m
	bgR := s * 185 / 1024
	barW, gap, maxH := s*118/1024, s*72/1024, s*460/1024
	totalW := 3*barW + 2*gap
	x0 := (s - totalW) / 2
	bottom := (s + maxH) / 2
	heights := [3]float64{8.0 / 13, 1, 10.0 / 13}
	ink := color.NRGBA{0x30, 0xD1, 0x58, 0xFF}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var bgc, barc float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					bgc += cover(sdRoundRect(px, py, bg0, bg0, bg1, bg1, bgR))
					for i, h := range heights {
						bx := x0 + float64(i)*(barW+gap)
						barc += cover(sdRoundRect(px, py, bx, bottom-maxH*h, bx+barW, bottom, barW*0.32))
					}
				}
			}
			bgc /= ss * ss
			barc = math.Min(1, barc/(ss*ss))
			// 背景：上深下略浅的渐变
			t := float64(y) / s
			base := color.NRGBA{uint8(34 + 14*t), uint8(36 + 14*t), uint8(42 + 16*t), 255}
			r := float64(base.R)*(1-barc) + float64(ink.R)*barc
			g := float64(base.G)*(1-barc) + float64(ink.G)*barc
			b := float64(base.B)*(1-barc) + float64(ink.B)*barc
			img.SetNRGBA(x, y, color.NRGBA{uint8(r), uint8(g), uint8(b), uint8(bgc * 255)})
		}
	}
	return img
}

func main() {
	dir, err := os.MkdirTemp("", "fluxwatch-iconset")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	set := filepath.Join(dir, "Fluxwatch.iconset")
	if err := os.Mkdir(set, 0o755); err != nil {
		panic(err)
	}
	for _, base := range []int{16, 32, 128, 256, 512} {
		for _, scale := range []int{1, 2} {
			name := "icon_" + itoa(base) + "x" + itoa(base)
			if scale == 2 {
				name += "@2x"
			}
			f, err := os.Create(filepath.Join(set, name+".png"))
			if err != nil {
				panic(err)
			}
			if err := png.Encode(f, render(base*scale)); err != nil {
				panic(err)
			}
			f.Close()
		}
	}
	if err := os.MkdirAll("scripts/assets", 0o755); err != nil {
		panic(err)
	}
	cmd := exec.Command("iconutil", "-c", "icns", set, "-o", "scripts/assets/Fluxwatch.icns")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		panic(err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
