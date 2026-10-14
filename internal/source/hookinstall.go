package source

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// hookCommand 认「调用 fluxwatch-cli hook」的命令：可执行文件名是 fluxwatch-cli（Windows 带 .exe，路径可带引号），
// 第一个参数是 hook，后面可以再跟事件名。README 的「二进制不在也不报错」写法
// `[ -x "$HOME/.fluxwatch/bin/fluxwatch-cli" ] && "$HOME/.fluxwatch/bin/fluxwatch-cli" hook || true` 也认。
var hookCommand = regexp.MustCompile(`(?i)(^|[\s"'/\\])fluxwatch-cli(\.exe)?["']?\s+hook(\s|$)`)

// hookBinary 从命令里取出被调用的 fluxwatch-cli 路径（引号里的或不带空格的一段）。
var hookBinary = regexp.MustCompile(`(?i)(?:"([^"]*fluxwatch-cli(?:\.exe)?)"|'([^']*fluxwatch-cli(?:\.exe)?)'|(\S*fluxwatch-cli(?:\.exe)?))\s+hook(?:\s|$)`)

// HookCommandOK 判断一条 hook 命令是不是在调 fluxwatch-cli hook。
func HookCommandOK(cmd string) bool { return hookCommand.MatchString(cmd) }

// HookCommandBinary 取出命令里调用的 fluxwatch-cli 路径（原样，不展开 $HOME、~）；取不到返回空。
func HookCommandBinary(cmd string) string {
	m := hookBinary.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	for _, s := range m[1:] {
		if s != "" {
			return s
		}
	}
	return ""
}

// ExtraEvents 是 PreToolUse / PostToolUse 之外 README 里一起装的事件：失败和被拒的调用靠它们结束（否则 pre 配不上 post），
// 回合事件判「在飞」、清残留调用。
var ExtraEvents = []string{"PostToolUseFailure", "UserPromptSubmit", "Stop", "StopFailure", "SubagentStop", "SessionEnd", "PermissionDenied"}

// HookInstall 是 Claude Code 用户设置里 fluxwatch hook 的安装情况。
type HookInstall struct {
	Pre, Post         bool     // 该事件下有一条对所有工具生效（matcher 为空或 *）的 fluxwatch-cli hook 命令
	PreOnly, PostOnly bool     // 挂了，但 matcher 只限部分工具
	Missing           []string // ExtraEvents 里没挂（或 matcher 只限一部分）的事件
	Binary            string   // 命令里调用的 fluxwatch-cli 路径（原样）；取不到为空
}

// Installed 表示 PreToolUse 和 PostToolUse 都对所有工具挂上了（能判本地忙）。
func (h HookInstall) Installed() bool { return h.Pre && h.Post }

type claudeHookEntry struct {
	Matcher *string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

// CheckHookInstall 读 ~/.claude/settings.json 的内容，按 README 的装法判断：
// hooks.PreToolUse 和 hooks.PostToolUse 下各有一条 type 为 command、命令调用 fluxwatch-cli hook 的 hook；
// ExtraEvents 里每个事件也各有一条（缺的记进 Missing）。只解析 hooks 这一段，别的设置不看。
func CheckHookInstall(settingsJSON []byte) (HookInstall, error) {
	var v struct {
		Hooks map[string][]claudeHookEntry `json:"hooks"`
	}
	if err := json.Unmarshal(settingsJSON, &v); err != nil {
		return HookInstall{}, fmt.Errorf("不是合法 JSON，或 hooks 的结构不对：%w", err)
	}
	var h HookInstall
	h.Pre, h.PreOnly = scanHookEntries(v.Hooks["PreToolUse"], &h.Binary)
	h.Post, h.PostOnly = scanHookEntries(v.Hooks["PostToolUse"], &h.Binary)
	for _, ev := range ExtraEvents {
		if all, _ := scanHookEntries(v.Hooks[ev], &h.Binary); !all {
			h.Missing = append(h.Missing, ev)
		}
	}
	return h, nil
}

func scanHookEntries(es []claudeHookEntry, bin *string) (all, partial bool) {
	for _, e := range es {
		for _, hk := range e.Hooks {
			if hk.Type != "command" || !HookCommandOK(hk.Command) {
				continue
			}
			if *bin == "" {
				*bin = HookCommandBinary(hk.Command)
			}
			if e.Matcher == nil || *e.Matcher == "" || *e.Matcher == "*" {
				all = true
			} else {
				partial = true
			}
		}
	}
	return all, partial
}

// ExpandHookBinary 把命令里的路径展开成本机路径：$HOME、${HOME}、%USERPROFILE%、开头的 ~ 换成 home。
// 不带目录的（只写了 fluxwatch-cli，靠 PATH 找）原样返回。
func ExpandHookBinary(p, home string) string {
	for _, v := range []string{"${HOME}", "$HOME", "%USERPROFILE%", "%userprofile%"} {
		p = strings.ReplaceAll(p, v, home)
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = home + p[1:]
	}
	return p
}
