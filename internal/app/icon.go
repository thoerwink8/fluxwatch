package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// 托盘图标：三条高低柱，四态共用一个轮廓，只换颜色。启动时用 Go 画成 PNG，不依赖外部素材。

// iconKind 是图标的四态。
type iconKind int

const (
	iconGood iconKind = iota // 正常（mac 上用模板图标）
	iconWarn                 // 可疑（也用于 unknown：读不到数据源，要人看一眼）
	iconCrit                 // 卡住（呼吸）
	iconBusy                 // 本地忙 / 空闲
)

// kindFor 把整体状态映射到图标。
func kindFor(worst model.State, anyBusy bool) iconKind {
	switch worst {
	case model.StateStall:
		return iconCrit
	case model.StateSuspect, model.StateUnknown:
		return iconWarn
	case model.StateOK, model.StateThinking:
		return iconGood
	}
	if anyBusy {
		return iconBusy
	}
	return iconGood
}

// 两套语义色，照 ui-sketch.html 的 token：明亮底（浅菜单栏 / 浅任务栏）和暗黑底分别调过。
var (
	paletteLight = map[iconKind]color.NRGBA{
		iconGood: {0x1F, 0x7A, 0x37, 0xFF}, // --good #1F7A37
		iconWarn: {0xA0, 0x48, 0x00, 0xFF}, // --warn #A04800
		iconCrit: {0xC7, 0x00, 0x13, 0xFF}, // --crit #C70013
		iconBusy: {0x5A, 0x5A, 0x5F, 0xFF}, // --busy #5A5A5F
	}
	paletteDark = map[iconKind]color.NRGBA{
		iconGood: {0x30, 0xD1, 0x58, 0xFF}, // #30D158
		iconWarn: {0xFF, 0x9F, 0x0A, 0xFF}, // #FF9F0A
		iconCrit: {0xFF, 0x45, 0x3A, 0xFF}, // #FF453A
		iconBusy: {0x98, 0x98, 0x9D, 0xFF}, // #98989D
	}
	// 中性字形色：Windows 正常态（任务栏没有模板图标机制，自己按任务栏明暗给字形色）
	neutralOnLight = color.NRGBA{0x1D, 0x1D, 0x1F, 0xFF}
	neutralOnDark  = color.NRGBA{0xF5, 0xF5, 0xF7, 0xFF}
	templateInk    = color.NRGBA{0, 0, 0, 0xFF}
	badgeTextColor = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
)

// barHeights 是三条柱子的相对高度（草图 18px 盒子里 8/13/10）。
var barHeights = [3]float64{8.0 / 13, 1, 10.0 / 13}

// iconSpec 描述一张要画的图标。
type iconSpec struct {
	size       int         // 画布边长（像素）
	ink        color.NRGBA // 柱子颜色
	alpha      float64     // 整体不透明度（呼吸帧用）
	badge      int         // 角标数字；0 不画（mac 用菜单栏文字做角标，不画进图标）
	badgeColor color.NRGBA
}

// renderIcon 画一张 PNG。柱子按草图比例：盒子 18 → 柱高 13、柱宽 2.6、间距 1.8、圆角 0.8。
func renderIcon(s iconSpec) []byte {
	const ss = 4 // 超采样倍数，做抗锯齿
	n := s.size * ss
	scale := float64(n) / 18.0
	barW, gap, rad, maxH := 2.6*scale, 1.8*scale, 0.8*scale, 13*scale
	if s.size <= 32 {
		// 小尺寸（Windows 16/32px）柱子加粗一点，免得糊掉
		barW, gap, maxH = 3.4*scale, 1.6*scale, 14*scale
	}
	totalW := 3*barW + 2*gap
	x0 := (float64(n) - totalW) / 2
	bottom := (float64(n) + maxH) / 2

	cov := make([]float64, n*n) // 柱子覆盖
	for i, h := range barHeights {
		bx := x0 + float64(i)*(barW+gap)
		fillRoundRect(cov, n, bx, bottom-maxH*h, bx+barW, bottom, rad)
	}

	var badgeCov, digitCov []float64
	if s.badge > 0 {
		badgeCov = make([]float64, n*n)
		digitCov = make([]float64, n*n)
		r := float64(n) * 0.25
		cx, cy := float64(n)-r-0.5, r+0.5
		fillCircle(badgeCov, n, cx, cy, r)
		// 角标挖空一圈，和柱子分开
		ring := make([]float64, n*n)
		fillCircle(ring, n, cx, cy, r+float64(n)*0.05)
		for i := range cov {
			cov[i] *= 1 - ring[i]
		}
		drawDigit(digitCov, n, cx, cy, r*1.15, s.badge)
	}

	img := image.NewNRGBA(image.Rect(0, 0, s.size, s.size))
	for y := 0; y < s.size; y++ {
		for x := 0; x < s.size; x++ {
			var c, b, d float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					i := (y*ss+sy)*n + x*ss + sx
					c += cov[i]
					if badgeCov != nil {
						b += badgeCov[i]
						d += digitCov[i]
					}
				}
			}
			c /= ss * ss
			b /= ss * ss
			d /= ss * ss
			px := blend(color.NRGBA{}, s.ink, c*s.alpha)
			if b > 0 {
				px = blend(px, s.badgeColor, b)
				px = blend(px, badgeTextColor, d*b)
			}
			img.SetNRGBA(x, y, px)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// blend 把 top 以覆盖率 a 叠到 dst 上（非预乘 alpha 的 over 运算）。
func blend(dst, top color.NRGBA, a float64) color.NRGBA {
	if a <= 0 {
		return dst
	}
	if a > 1 {
		a = 1
	}
	ta := a * float64(top.A) / 255
	da := float64(dst.A) / 255
	oa := ta + da*(1-ta)
	if oa == 0 {
		return color.NRGBA{}
	}
	mix := func(t, d uint8) uint8 {
		v := (float64(t)*ta + float64(d)*da*(1-ta)) / oa
		return uint8(math.Round(v))
	}
	return color.NRGBA{mix(top.R, dst.R), mix(top.G, dst.G), mix(top.B, dst.B), uint8(math.Round(oa * 255))}
}

func fillRoundRect(cov []float64, n int, x0, y0, x1, y1, r float64) {
	for y := 0; y < n; y++ {
		py := float64(y) + 0.5
		if py < y0 || py > y1 {
			continue
		}
		for x := 0; x < n; x++ {
			px := float64(x) + 0.5
			if px < x0 || px > x1 {
				continue
			}
			// 四个角按圆裁
			cx := math.Max(x0+r, math.Min(px, x1-r))
			cy := math.Max(y0+r, math.Min(py, y1-r))
			if (px-cx)*(px-cx)+(py-cy)*(py-cy) <= r*r {
				cov[y*n+x] = 1
			}
		}
	}
}

func fillCircle(cov []float64, n int, cx, cy, r float64) {
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				cov[y*n+x] = 1
			}
		}
	}
}

// 3×5 点阵数字，够角标用；大于 9 画「9」上加一点（记作 9+ 的意思）。
var digits = [10][5]string{
	{"111", "101", "101", "101", "111"},
	{"010", "110", "010", "010", "111"},
	{"111", "001", "111", "100", "111"},
	{"111", "001", "111", "001", "111"},
	{"101", "101", "111", "001", "001"},
	{"111", "100", "111", "001", "111"},
	{"111", "100", "111", "101", "111"},
	{"111", "001", "010", "010", "010"},
	{"111", "101", "111", "101", "111"},
	{"111", "101", "111", "001", "111"},
}

func drawDigit(cov []float64, n int, cx, cy, h float64, v int) {
	if v > 9 {
		v = 9
	}
	cell := h / 5
	x0, y0 := cx-1.5*cell, cy-2.5*cell
	for row, line := range digits[v] {
		for col, ch := range line {
			if ch != '1' {
				continue
			}
			fillRoundRect(cov, n, x0+float64(col)*cell, y0+float64(row)*cell,
				x0+float64(col+1)*cell, y0+float64(row+1)*cell, 0)
		}
	}
}

// breathAlpha 是卡住态第 i 帧（共 frames 帧，一个周期 1.9s）的不透明度：1 → 0.42 → 1。
func breathAlpha(i, frames int) float64 {
	phase := float64(i) / float64(frames) * 2 * math.Pi
	return 0.71 + 0.29*math.Cos(phase)
}
