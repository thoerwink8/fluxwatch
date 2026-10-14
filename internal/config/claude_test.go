package config

import (
	"strings"
	"testing"
)

// #27：目标写明哪些 Claude 会话的模型流量走它的线路。
func TestClaudeFieldParse(t *testing.T) {
	c, err := Parse([]byte(`targets:
  - name: a
    claude: all
    sources: [ { type: mihomo, hosts: ["*.example.com"] } ]
  - name: b
    claude: [Mirasim, " reclaude "]
    sources: [ { type: mihomo, hosts: ["*.example.org"] } ]
  - name: c
    sources: [ { type: mihomo, hosts: ["*.example.net"] } ]
`))
	if err != nil {
		t.Fatal(err)
	}
	a, b, cc := c.Targets[0].Claude, c.Targets[1].Claude, c.Targets[2].Claude
	if a == nil || !a.All || len(a.Apps) != 0 {
		t.Fatalf("claude: all：%+v", a)
	}
	if b == nil || b.All || len(b.Apps) != 2 || b.Apps[1] != "reclaude" {
		t.Fatalf("claude: [..]：%+v", b)
	}
	if cc != nil {
		t.Fatalf("不写就是不接 hook：%+v", cc)
	}
	for _, bad := range []string{"claude: some", "claude: []", `claude: [""]`, "claude: {x: 1}"} {
		_, err := Parse([]byte("targets:\n  - name: a\n    " + bad + "\n    sources: [ { type: mihomo, hosts: [\"*.example.com\"] } ]\n"))
		if err == nil || !strings.Contains(err.Error(), "claude") {
			t.Fatalf("%s 应当报错并提到 claude：%v", bad, err)
		}
	}
}

// 默认配置：reclaude 接所有 Claude 会话、挂 launch.log 的回合；mirasim 只管云端中转，不接 hook、不挂回合。
func TestDefaultTargetsRouting(t *testing.T) {
	c, err := Parse([]byte(DefaultYAML))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Target{}
	for _, tg := range c.Targets {
		by[tg.Name] = tg
	}
	rc, ms := by["reclaude"], by["mirasim"]
	if rc.Claude == nil || !rc.Claude.All {
		t.Fatalf("reclaude 要 claude: all：%+v", rc.Claude)
	}
	turn := false
	for _, s := range rc.Sources {
		if s.Type == TypeLogtail && strings.Contains(s.Path, "launch.log") && s.Start != "" && s.End != "" {
			turn = true
		}
	}
	if !turn {
		t.Fatal("reclaude 要挂 launch.log 的回合正则")
	}
	if ms.Claude != nil {
		t.Fatalf("mirasim 不接 hook：%+v", ms.Claude)
	}
	for _, s := range ms.Sources {
		if s.Type == TypeLogtail {
			t.Fatalf("mirasim 不挂日志回合：%+v", s)
		}
	}
	if len(ms.Sources) != 1 || ms.Sources[0].Hosts[0] != "*.mirasim.ai" || ms.Sources[0].Apps[0] != "Mirasim" {
		t.Fatalf("mirasim 只管 *.mirasim.ai + Mirasim：%+v", ms.Sources)
	}
	if !c.AnyClaude() {
		t.Fatal("默认配置接了 hook")
	}
	old, _ := Parse([]byte("targets:\n  - name: a\n    sources: [ { type: mihomo, hosts: [\"*.example.com\"] } ]\n"))
	if old.AnyClaude() {
		t.Fatal("老配置没写 claude，AnyClaude 为假")
	}
}
