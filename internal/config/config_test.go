package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenWritesDefaultTwoTargets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "targets.yaml")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if len(c.Targets) != 2 || c.Targets[0].Name != "reclaude" || c.Targets[1].Name != "mirasim" {
		t.Fatalf("默认目标 = %+v", c.Targets)
	}
	if c.Defaults.Suspect.Std() != 10*time.Second || c.Defaults.Stall.Std() != 30*time.Second {
		t.Fatalf("默认阈值 = %+v", c.Defaults)
	}
	if c.Targets[0].Sources[2].Progress == "" { // #27：launch.log 的回合挂在 reclaude 上
		t.Fatal("launch.log 的 progress 正则丢了")
	}
}

func TestAddRemoveWriteBackAndReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "targets.yaml")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	name, err := s.AddTarget("Example API", "*.Example.com")
	if err != nil || name != "example-api" {
		t.Fatalf("add = %q %v", name, err)
	}
	name2, err := s.AddTarget("Example API", "*.example.org")
	if err != nil || name2 != "example-api-2" {
		t.Fatalf("重名 = %q %v", name2, err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s2.Get().Targets); got != 4 {
		t.Fatalf("写回后重读有 %d 个目标", got)
	}
	if err := s.RemoveTarget("example-api"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTarget("nope"); err == nil {
		t.Fatal("删不存在的目标应当报错")
	}

	// 手改 → 热加载
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "label: ReClaude", "label: RC", 1)), 0o600)
	future := time.Now().Add(time.Hour)
	os.Chtimes(p, future, future)
	changed, err := s.Reload()
	if err != nil || !changed || s.Get().Targets[0].Label != "RC" {
		t.Fatalf("热加载 changed=%v err=%v label=%q", changed, err, s.Get().Targets[0].Label)
	}
	// 改坏 → 保留旧的并报错
	os.WriteFile(p, []byte("targets:\n  - { name: x, sources: [ { type: nope } ] }\n"), 0o600)
	os.Chtimes(p, future.Add(time.Hour), future.Add(time.Hour))
	if _, err := s.Reload(); err == nil || s.Get().Targets[0].Label != "RC" {
		t.Fatalf("坏配置应当报错并保留旧的：%v", err)
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cache, _ := os.UserCacheDir()
	if got, _ := ExpandPath("~/logs/a.log"); got != filepath.Join(home, "logs", "a.log") {
		t.Fatalf("~ 展开 = %s", got)
	}
	if got, _ := ExpandPath("<UserCacheDir>/x/launch.log"); got != filepath.Join(cache, "x", "launch.log") {
		t.Fatalf("<UserCacheDir> 展开 = %s", got)
	}
}

func TestSettingsWatcher(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	w := NewSettingsWatcher(p)
	if s, err := w.Get(); s != nil || err != nil {
		t.Fatal("没有文件：nil, nil")
	}
	os.WriteFile(p, []byte(`{"mihomoMode":"manual","mihomoAddr":" 127.0.0.1:9090 ","mihomoSecret":"x","suspectSec":12,"stallSec":40}`), 0o600)
	s, err := w.Get()
	if err != nil {
		t.Fatal(err)
	}
	if a, sec := s.Manual(); a != "127.0.0.1:9090" || sec != "x" {
		t.Fatalf("manual = %q %q", a, sec)
	}
	if su, st := s.Thresholds(); su != 12*time.Second || st != 40*time.Second {
		t.Fatalf("阈值 = %v %v", su, st)
	}
	auto := &UISettings{MihomoMode: "auto", MihomoAddr: "1.2.3.4:9"}
	if a, _ := auto.Manual(); a != "" {
		t.Fatal("自动模式不该用地址")
	}
}

func TestDefaultTargetsFilterByApp(t *testing.T) {
	c, err := Parse([]byte(DefaultYAML))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"reclaude": "reclaude", "mirasim": "Mirasim"}
	for _, tg := range c.Targets {
		src := tg.Sources[0]
		if src.Type != TypeMihomo || len(src.Apps) != 1 || src.Apps[0] != want[tg.Name] {
			t.Fatalf("%s 的默认 mihomo 数据源 apps = %v，要 [%s]", tg.Name, src.Apps, want[tg.Name])
		}
	}
}

func TestOpenKeepsExistingFileUntouched(t *testing.T) {
	p := filepath.Join(t.TempDir(), "targets.yaml")
	old := "targets:\n  - name: reclaude\n    sources:\n      - { type: mihomo, hosts: [\"*.reclaude.ai\"] }\n"
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != old {
		t.Fatalf("已有的 targets.yaml 被改写了：\n%s", b)
	}
	if apps := s.Get().Targets[0].Sources[0].Apps; len(apps) != 0 {
		t.Fatalf("老配置没写 apps 就不该限软件：%v", apps)
	}
}

func TestAppsValidateAndAddTargetWithApps(t *testing.T) {
	if _, err := Parse([]byte("targets:\n  - { name: x, sources: [ { type: logtail, path: /tmp/a.log, apps: [x] } ] }\n")); err == nil {
		t.Fatal("logtail 上写 apps 应当报错")
	}
	if _, err := Parse([]byte("targets:\n  - { name: x, sources: [ { type: mihomo, hosts: [a.com], apps: [\" \"] } ] }\n")); err == nil {
		t.Fatal("空软件名应当报错")
	}

	p := filepath.Join(t.TempDir(), "targets.yaml")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// 默认 reclaude 目标只算 reclaude 软件：浏览器的同域名可以单独建目标
	name, err := s.AddTarget("ReClaude 网页", "*.reclaude.ai", " ego lite ", "", "Ego Lite")
	if err != nil {
		t.Fatalf("软件不重叠应当允许：%v", err)
	}
	var added Target
	for _, tg := range s.Get().Targets {
		if tg.Name == name {
			added = tg
		}
	}
	if got := added.Sources[0].Apps; len(got) != 1 || got[0] != "ego lite" {
		t.Fatalf("写进去的 apps = %v，要 [ego lite]", got)
	}
	if _, err := s.AddTarget("again", "*.reclaude.ai", "ReClaude"); err == nil {
		t.Fatal("同域名、同软件（不分大小写）应当拒绝")
	}
	if _, err := s.AddTarget("all", "*.reclaude.ai"); err == nil {
		t.Fatal("同域名、不限软件会和已有目标重叠，应当拒绝")
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "ego lite") {
		t.Fatalf("没写回文件：%s", b)
	}
}

// AddTarget / RemoveTarget 写回时保留注释和原来的写法（#8）。
func TestAddRemoveKeepsComments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "targets.yaml")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	hand := strings.Replace(string(b), "  - name: mirasim\n", "  # 我的手写注释：mirasim 走美国节点\n  - name: mirasim # 行尾注释\n", 1)
	if err := os.WriteFile(p, []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTarget("Example", "*.example.com", "Example App"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	out := string(b)
	for _, want := range []string{
		"# Fluxwatch 监控目标。", "# mihomo: { controller:", "# 我的手写注释：mirasim 走美国节点", "# 行尾注释",
		// yaml.v3 重新排版流式写法的空格（{ a } → {a}），内容和注释不变
		"defaults: {suspect: 10s, stall: 30s}",
		`- {type: mihomo, hosts: ['*.example.com'], apps: [Example App]}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("写回后少了 %q：\n%s", want, out)
		}
	}
	if err := s.RemoveTarget("example"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), "example.com") || !strings.Contains(string(b), "# 我的手写注释") {
		t.Fatalf("删目标后：\n%s", b)
	}
	if c := s.Get(); len(c.Targets) != 2 || c.Targets[0].Sources[2].Start == "" || c.Targets[0].Claude == nil {
		t.Fatalf("删完应当回到默认两个目标：%+v", c.Targets)
	}
	// targets 是空的流式列表、或者干脆没写也能加
	os.WriteFile(p, []byte("# 只有注释\ndefaults: { suspect: 10s, stall: 30s }\ntargets: []\n"), 0o600)
	if _, err := s.AddTarget("", "*.example.org"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), "# 只有注释") || len(s.Get().Targets) != 1 {
		t.Fatalf("空列表上加：\n%s", b)
	}
}
