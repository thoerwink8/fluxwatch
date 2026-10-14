package app

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

func TestRenderIconDecodesAndKeepsOutline(t *testing.T) {
	a := renderIcon(iconSpec{size: 44, ink: paletteLight[iconCrit], alpha: 1})
	b := renderIcon(iconSpec{size: 44, ink: paletteLight[iconGood], alpha: 1})
	ia, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	ib, _ := png.Decode(bytes.NewReader(b))
	if ia.Bounds().Dx() != 44 || ia.Bounds().Dy() != 44 {
		t.Fatalf("尺寸 %v", ia.Bounds())
	}
	// 四态共用轮廓：两张图每个像素的 alpha 一样，只是颜色不同
	inked := 0
	for y := 0; y < 44; y++ {
		for x := 0; x < 44; x++ {
			_, _, _, aa := ia.At(x, y).RGBA()
			_, _, _, ab := ib.At(x, y).RGBA()
			if aa != ab {
				t.Fatalf("(%d,%d) 轮廓不同：%d vs %d", x, y, aa, ab)
			}
			if aa > 0 {
				inked++
			}
		}
	}
	if inked < 100 {
		t.Fatalf("几乎没画东西：%d 像素", inked)
	}
}

func TestBreathAlphaRange(t *testing.T) {
	lo, hi := 1.0, 0.0
	for i := 0; i < breathFrames; i++ {
		a := breathAlpha(i, breathFrames)
		lo, hi = min(lo, a), max(hi, a)
	}
	if hi < 0.99 || lo > 0.45 || lo < 0.41 {
		t.Fatalf("呼吸范围 %.2f–%.2f，应约 0.42–1", lo, hi)
	}
}

func TestBadgeChangesIcon(t *testing.T) {
	plain := renderIcon(iconSpec{size: 32, ink: neutralOnDark, alpha: 1})
	badged := renderIcon(iconSpec{size: 32, ink: neutralOnDark, alpha: 1, badge: 2, badgeColor: paletteDark[iconCrit]})
	if bytes.Equal(plain, badged) {
		t.Fatal("角标没画上")
	}
}

func TestKindFor(t *testing.T) {
	cases := []struct {
		worst model.State
		busy  bool
		want  iconKind
	}{
		{model.StateStall, false, iconCrit},
		{model.StateSuspect, false, iconWarn},
		{model.StateUnknown, false, iconWarn},
		{model.StateOK, true, iconGood}, // 本地忙不参与：有正常的就显示正常
		{model.StateIdle, true, iconBusy},
		{model.StateIdle, false, iconGood},
	}
	for _, c := range cases {
		if got := kindFor(c.worst, c.busy); got != c.want {
			t.Errorf("kindFor(%s,%v)=%d want %d", c.worst, c.busy, got, c.want)
		}
	}
}

func snap(states ...model.State) model.Snapshot {
	var s model.Snapshot
	for i, st := range states {
		s.Targets = append(s.Targets, model.Target{Name: string(rune('a' + i)), Label: "T", State: st, StuckFor: 31, Node: "n1", Layer: model.LayerLine})
	}
	return s
}

func TestTransitionsOncePerEdge(t *testing.T) {
	tr := newTransitions()
	if a := tr.next(snap(model.StateStall)); len(a) != 0 {
		t.Fatalf("启动时已在卡住不补发：%v", a)
	}
	recovered := snap(model.StateOK)
	recovered.Targets[0].Recovered = true // 引擎在真进展那一轮标恢复，通知只认它
	if a := tr.next(recovered); len(a) != 1 || a[0].kind != alertRecover {
		t.Fatalf("恢复应发一次：%v", a)
	}
	if a := tr.next(snap(model.StateSuspect)); len(a) != 0 {
		t.Fatalf("可疑不发：%v", a)
	}
	if a := tr.next(snap(model.StateStall)); len(a) != 1 || a[0].kind != alertStall {
		t.Fatalf("进卡住应发一次：%v", a)
	}
	if a := tr.next(snap(model.StateStall)); len(a) != 0 {
		t.Fatalf("持续卡住不重复发：%v", a)
	}
	if a := tr.next(snap(model.StateUnknown)); len(a) != 0 {
		t.Fatalf("卡住→读不到不算恢复：%v", a)
	}
}

func TestAlertMessageHasNoSessionContent(t *testing.T) {
	a := alert{kind: alertStall, target: model.Target{Label: "ReClaude", StuckFor: 42, Layer: model.LayerLine, Node: "节点04", Reason: "secret prompt text"}}
	title, body := a.message()
	if !strings.Contains(title, "ReClaude") || !strings.Contains(body, "A 线路停") || !strings.Contains(body, "42s") {
		t.Fatalf("%q %q", title, body)
	}
	if strings.Contains(title+body, "secret") {
		t.Fatal("通知正文不该带 Reason 之外的东西，更不能带会话内容")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "settings.json")
	st := newSettingsStore(path)
	if err := st.load(); err != nil {
		t.Fatal(err)
	}
	if got := st.get(); got != DefaultSettings() {
		t.Fatalf("默认值不对：%+v", got)
	}
	s := DefaultSettings()
	s.Notify = false
	s.LaunchAtLogin = true
	if err := st.save(s); err != nil {
		t.Fatal(err)
	}
	st2 := newSettingsStore(path)
	if err := st2.load(); err != nil {
		t.Fatal(err)
	}
	if st2.get() != s {
		t.Fatalf("读回来不一样：%+v", st2.get())
	}
	bad := s
	bad.StallSec = bad.SuspectSec
	if err := st.save(bad); err == nil {
		t.Fatal("卡住阈值不大于可疑阈值时应拒绝")
	}
	bad = s
	bad.MihomoMode = "manual"
	if err := st.save(bad); err == nil {
		t.Fatal("手填但没地址应拒绝")
	}
}

func TestSettingsCorruptFileKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newSettingsStore(path)
	if err := st.load(); err == nil {
		t.Fatal("坏文件应报错")
	}
	if st.get() != DefaultSettings() {
		t.Fatal("坏文件时用默认值")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "{oops" {
		t.Fatal("不能悄悄覆盖用户的文件")
	}
}

func TestHookStatus(t *testing.T) {
	home := t.TempDir()
	if hs := hookStatus(home); hs.Installed {
		t.Fatal("没有设置文件不该算装了")
	}
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	write := func(cfg string) HookStatus {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		return hookStatus(home)
	}
	cfg := `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"$HOME/.fluxwatch/bin/fluxwatch-cli hook"}]}],"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"fluxwatch-cli hook post"}]}]}}`
	if hs := write(cfg); !hs.Installed {
		t.Fatalf("应识别为已装：%+v", hs)
	}
	// 只挂了 PreToolUse；命令里只是出现 fluxwatch 字样不算
	cfg = `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"fluxwatch-cli hook"}]}],"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo fluxwatch"}]}]}}`
	if hs := write(cfg); hs.Installed || !strings.Contains(hs.Detail, "一半") {
		t.Fatalf("只挂一半：%+v", hs)
	}
	// matcher 只限 Bash：不算装好
	cfg = `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"fluxwatch-cli hook"}]}],"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"fluxwatch-cli hook"}]}]}}`
	if hs := write(cfg); hs.Installed || !strings.Contains(hs.Detail, "matcher") {
		t.Fatalf("matcher 只限部分工具：%+v", hs)
	}
	if hs := write(`{"hooks":`); hs.Installed || !strings.Contains(hs.Detail, "读不懂") {
		t.Fatalf("坏 JSON：%+v", hs)
	}
}

func TestHistoryMergeAndWindow(t *testing.T) {
	h := newHistory()
	base := time.Unix(1_700_000_000, 0)
	mk := func(from, n int) []model.Sample {
		var out []model.Sample
		for i := 0; i < n; i++ {
			out = append(out, model.Sample{At: base.Add(time.Duration(from+i) * time.Second), Down: int64(from + i)})
		}
		return out
	}
	h.merge(model.Snapshot{Targets: []model.Target{{Name: "a", Trace: mk(0, 900)}}})
	h.merge(model.Snapshot{Targets: []model.Target{{Name: "a", Trace: mk(1, 900)}}}) // 只多一点
	if got := len(h.last("a", 0)); got != 901 {
		t.Fatalf("应去重拼接成 901 点，得 %d", got)
	}
	if got := h.last("a", 60); len(got) != 60 || got[59].Down != 900 {
		t.Fatalf("最近 60 秒取错：%d", len(got))
	}
	h.merge(model.Snapshot{Targets: []model.Target{{Name: "a", Trace: mk(900, historyLen)}}})
	if got := len(h.last("a", 0)); got != historyLen {
		t.Fatalf("超过 1 小时要截断，得 %d", got)
	}
	h.merge(model.Snapshot{})
	if got := len(h.last("a", 0)); got != 0 {
		t.Fatal("目标没了历史要清掉")
	}
}

func TestNormalIconFollowsMenuBarShade(t *testing.T) {
	// 正常态不用模板图标：亮菜单栏画深色字形、暗菜单栏画浅色；其他态仍用语义色
	light, dark := buildIconSet(false, 44), buildIconSet(true, 44)
	ink := func(b []byte) (r, g, bl uint32) {
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r, g, bl, _ = img.At(22, 22).RGBA() // 中间那根柱子
		return r >> 8, g >> 8, bl >> 8
	}
	if r, g, b := ink(light.still[iconGood]); r > 0x40 || g > 0x40 || b > 0x40 {
		t.Fatalf("亮菜单栏上的正常态应当是深色：%02x%02x%02x", r, g, b)
	}
	if r, g, b := ink(dark.still[iconGood]); r < 0xE0 || g < 0xE0 || b < 0xE0 {
		t.Fatalf("暗菜单栏上的正常态应当是浅色：%02x%02x%02x", r, g, b)
	}
	if r, _, _ := ink(light.still[iconCrit]); r != uint32(paletteLight[iconCrit].R) {
		t.Fatalf("卡住态应当用语义红：r=%02x", r)
	}
}

func TestTrayPlacementNotch(t *testing.T) {
	// 实测：屏宽 1512，刘海约 655–857，图标落在 x=694
	notch := trayPlacement{Found: true, Visible: true, ScreenW: 1512, HasNotch: true, NotchL: 655, NotchR: 857}
	cases := []struct {
		x, w   float64
		in, ok bool
	}{
		{694, 30, true, false},
		{640, 30, true, false}, // 被挡住一半也算
		{600, 30, false, true},
		{900, 30, false, true},
	}
	for _, c := range cases {
		p := notch
		p.X, p.W = c.x, c.w
		if p.InNotch() != c.in || p.usable() != c.ok {
			t.Errorf("x=%.0f：InNotch=%v usable=%v，要 %v %v", c.x, p.InNotch(), p.usable(), c.in, c.ok)
		}
	}
	noNotch := trayPlacement{Found: true, Visible: true, X: 694, W: 30}
	if noNotch.InNotch() || !noNotch.usable() {
		t.Fatal("没有刘海的屏幕不该判进刘海")
	}
	if (trayPlacement{}).usable() {
		t.Fatal("没找到状态项时不能贴")
	}
	_, body := notchMessage()
	if !strings.Contains(body, "刘海") || !strings.Contains(body, hotkeyLabel) || !strings.Contains(body, "⌘ 把它拖到右边") {
		t.Fatalf("通知文案：%s", body)
	}
}

func TestSplitApps(t *testing.T) {
	got := splitApps([]string{" reclaude ", "", "  ", "ego lite"})
	if len(got) != 2 || got[0] != "reclaude" || got[1] != "ego lite" {
		t.Fatalf("splitApps = %q", got)
	}
	if splitApps(nil) != nil {
		t.Fatal("空的应当是 nil")
	}
}
