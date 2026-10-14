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
	if c.Targets[1].Sources[1].Progress == "" {
		t.Fatal("mirasim 的 progress 正则丢了")
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
