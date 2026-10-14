package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

// 托盘图标：三条高低柱，正常、可疑、卡住、本地忙共用一个轮廓，只换颜色；读不到（unknown）换成灰色问号，
// 不和「慢」「卡住」混。启动时用 Go 画成 PNG，不依赖外部素材。

// iconKind 是图标的五态。
type iconKind int

const (
	iconGood    iconKind = iota // 正常（中性单色，按菜单栏 / 任务栏明暗画）
	iconWarn                    // 可疑
	iconCrit                    // 卡住（呼吸）
	iconBusy                    // 本地忙 / 空闲
	iconUnknown                 // 读不到数据源（Clash 没开、配置坏了）：灰色问号，不像在报警
)

// kindFor 把整体状态映射到图标。
func kindFor(worst model.State, anyBusy bool) iconKind {
	switch worst {
	case model.StateStall:
		return iconCrit
	case model.StateSuspect:
		return iconWarn
	case model.StateUnknown:
		return iconUnknown
	case model.StateOK, model.StateThinking:
		return iconGood
	}
	if anyBusy {
		return iconBusy
	}
	return iconGood
}

// trayView 是托盘图标要的整体状态和角标，在界面层算：卡住 > 慢 > 读不到 > 正常。
// 引擎的 Worst 里读不到排在慢前面（Severity），图标若照它，一个目标读不到就把另一个目标的「慢」盖成问号；
// 角标只数卡住和慢，读不到不算报警。
func trayView(s model.Snapshot) (worst model.State, badge int) {
	rank := func(st model.State) int {
		switch st {
		case model.StateStall:
			return 4
		case model.StateSuspect:
			return 3
		case model.StateUnknown:
			return 2
		case model.StateOK, model.StateThinking:
			return 1
		}
		return 0
	}
	worst = model.StateIdle
	if s.Worst == model.StateUnknown && len(s.Targets) == 0 {
		worst = model.StateUnknown // 引擎起不来：没有目标，只有原因
	}
	for _, t := range s.Targets {
		if rank(t.State) > rank(worst) {
			worst = t.State
		}
		if t.State == model.StateStall || t.State == model.StateSuspect {
			badge++
		}
	}
	return worst, badge
}

// 两套语义色，照 ui-sketch.html 的 token：明亮底（浅菜单栏 / 浅任务栏）和暗黑底分别调过。
var (
	paletteLight = map[iconKind]color.NRGBA{
		iconGood:    {0x1F, 0x7A, 0x37, 0xFF}, // --good #1F7A37
		iconWarn:    {0xA0, 0x48, 0x00, 0xFF}, // --warn #A04800
		iconCrit:    {0xC7, 0x00, 0x13, 0xFF}, // --crit #C70013
		iconBusy:    {0x5A, 0x5A, 0x5F, 0xFF}, // --busy #5A5A5F
		iconUnknown: {0x8E, 0x8E, 0x93, 0xFF}, // 灰：比 --busy 浅一档，问号
	}
	paletteDark = map[iconKind]color.NRGBA{
		iconGood:    {0x30, 0xD1, 0x58, 0xFF}, // #30D158
		iconWarn:    {0xFF, 0x9F, 0x0A, 0xFF}, // #FF9F0A
		iconCrit:    {0xFF, 0x45, 0x3A, 0xFF}, // #FF453A
		iconBusy:    {0x98, 0x98, 0x9D, 0xFF}, // #98989D
		iconUnknown: {0x8E, 0x8E, 0x93, 0xFF}, // 灰
	}
	// 中性字形色：正常态（不用模板图标，自己按菜单栏 / 任务栏明暗给字形色）
	neutralOnLight = color.NRGBA{0x1D, 0x1D, 0x1F, 0xFF}
	neutralOnDark  = color.NRGBA{0xF5, 0xF5, 0xF7, 0xFF}
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
	question   bool // 画问号代替三条柱（读不到）
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
	if s.question {
		fillQuestion(cov, n, scale, s.size <= 32)
	} else {
		for i, h := range barHeights {
			bx := x0 + float64(i)*(barW+gap)
			fillRoundRect(cov, n, bx, bottom-maxH*h, bx+barW, bottom, rad)
		}
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

// fillQuestion 画一个问号：上面一段圆弧（从左边绕过顶到右下），接一小段竖笔，底下一个点。
// 坐标按 18 的盒子（scale 换算到画布），和三条柱同高（柱高 13，从 2.5 到 15.5）。
func fillQuestion(cov []float64, n int, scale float64, small bool) {
	stroke := 2.2
	if small {
		stroke = 2.8 // Windows 16/32px 加粗
	}
	cx, cy, r := 9.0, 6.4, 3.4 // 弧心、半径（笔画中线）
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			px, py := (float64(x)+0.5)/scale, (float64(y)+0.5)/scale
			dx, dy := px-cx, py-cy
			d := math.Hypot(dx, dy)
			if math.Abs(d-r) <= stroke/2 {
				// 屏幕坐标里的角度：0 在右、顺时针为正。留左上到右下这一段（-180°…+60°）
				a := math.Atan2(dy, dx) * 180 / math.Pi
				if a <= 60 || a >= 175 {
					cov[y*n+x] = 1
				}
			}
		}
	}
	// 弧尾（60°）往下接到中线的竖笔
	ex := cx + r*math.Cos(math.Pi/3)
	ey := cy + r*math.Sin(math.Pi/3)
	fillSegment(cov, n, scale, ex, ey, cx, ey+1.6, stroke/2)
	fillSegment(cov, n, scale, cx, ey+1.6, cx, 11.6, stroke/2)
	fillCircle(cov, n, cx*scale, 14.2*scale, stroke*0.62*scale)
}

// fillSegment 画一段圆头线段（盒子坐标，半宽 hw）。
func fillSegment(cov []float64, n int, scale, x0, y0, x1, y1, hw float64) {
	vx, vy := x1-x0, y1-y0
	l2 := vx*vx + vy*vy
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			px, py := (float64(x)+0.5)/scale, (float64(y)+0.5)/scale
			t := 0.0
			if l2 > 0 {
				t = math.Max(0, math.Min(1, ((px-x0)*vx+(py-y0)*vy)/l2))
			}
			if math.Hypot(px-(x0+t*vx), py-(y0+t*vy)) <= hw {
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
