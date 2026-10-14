package source

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// hookCommand 认「调用 fluxwatch-cli hook」的命令：可执行文件名是 fluxwatch-cli（Windows 带 .exe，路径可带引号），
// 第一个参数是 hook，后面可以再跟 pre / post。
var hookCommand = regexp.MustCompile(`(?i)(^|[\s"'/\\])fluxwatch-cli(\.exe)?["']?\s+hook(\s|$)`)

// HookCommandOK 判断一条 hook 命令是不是在调 fluxwatch-cli hook。
func HookCommandOK(cmd string) bool { return hookCommand.MatchString(cmd) }

// HookInstall 是 Claude Code 用户设置里 fluxwatch hook 的安装情况。
type HookInstall struct {
	Pre, Post         bool // 该事件下有一条对所有工具生效（matcher 为空或 *）的 fluxwatch-cli hook 命令
	PreOnly, PostOnly bool // 挂了，但 matcher 只限部分工具
}

// Installed 表示 PreToolUse 和 PostToolUse 都对所有工具挂上了。
func (h HookInstall) Installed() bool { return h.Pre && h.Post }

type claudeHookEntry struct {
	Matcher *string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

// CheckHookInstall 读 ~/.claude/settings.json 的内容，按 README 的装法判断：
// hooks.PreToolUse 和 hooks.PostToolUse 下各有一条 type 为 command、命令调用 fluxwatch-cli hook 的 hook。
// 只解析 hooks 这一段，别的设置不看。
func CheckHookInstall(settingsJSON []byte) (HookInstall, error) {
	var v struct {
		Hooks map[string][]claudeHookEntry `json:"hooks"`
	}
	if err := json.Unmarshal(settingsJSON, &v); err != nil {
		return HookInstall{}, fmt.Errorf("不是合法 JSON，或 hooks 的结构不对：%w", err)
	}
	var h HookInstall
	h.Pre, h.PreOnly = scanHookEntries(v.Hooks["PreToolUse"])
	h.Post, h.PostOnly = scanHookEntries(v.Hooks["PostToolUse"])
	return h, nil
}

func scanHookEntries(es []claudeHookEntry) (all, partial bool) {
	for _, e := range es {
		for _, hk := range e.Hooks {
			if hk.Type != "command" || !HookCommandOK(hk.Command) {
				continue
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
