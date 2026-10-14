// Package source 是三种数据源：mihomo（按 host 通配挑连接）、logtail（跟读日志）、hook（本地工具在不在跑）。
// 每种数据源只回答「有没有进展」「有没有报错」；判定在 engine 里。
package source

import (
	"path"
	"strings"
)

// MatchHost 判断 host 是否匹配通配：「*.example.com」匹配 example.com 本身和所有子域；其他写法按 path.Match。
func MatchHost(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if pattern == "" || host == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(pattern, "*."); ok && !strings.ContainsAny(rest, "*?[") {
		return host == rest || strings.HasSuffix(host, "."+rest)
	}
	ok, _ := path.Match(pattern, host)
	return ok
}

// MatchAny 任一通配匹配即可。
func MatchAny(patterns []string, host string) bool {
	for _, p := range patterns {
		if MatchHost(p, host) {
			return true
		}
	}
	return false
}

// MatchApp 判断软件名是否在 apps 里：不分大小写，Windows 的 .exe 后缀不算。apps 为空表示不限软件。
func MatchApp(apps []string, app string) bool {
	if len(apps) == 0 {
		return true
	}
	a := normApp(app)
	if a == "" {
		return false
	}
	for _, x := range apps {
		if normApp(x) == a {
			return true
		}
	}
	return false
}

func normApp(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ".exe")
}
