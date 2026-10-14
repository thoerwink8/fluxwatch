package source

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// #21 M2：会话还没读到过回合事件（装新事件之前起的会话，Claude Code 只在会话开始时读 hook 配置），
// 它的回合结束不会有 Stop：post 之后的「在飞」永远收不了尾。这种会话不进 Flight，引擎对它退回不靠 hook 的判法。
func TestHookFlightOnlyForSessionsWithRounds(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	mira := []string{"Mirasim"}
	// 老会话：只有 pre / post
	mustAppend(t, p, hookEv("PreToolUse", "old", "", "a"), t0, "Mirasim")
	mustAppend(t, p, hookEv("PostToolUse", "old", "", "a"), t0.Add(time.Second), "Mirasim")
	// 新会话：有回合事件
	mustAppend(t, p, hookEv("UserPromptSubmit", "new", "", ""), t0.Add(5*time.Second), "Mirasim")
	r, err := h.Running(t0.Add(6 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if since, ok := r.FlightSince(mira); !ok || !since.Equal(t0.Add(5*time.Second)) {
		t.Fatalf("只算有回合事件的会话：在飞起点 %v（%v），要 %v", since, ok, t0.Add(5*time.Second))
	}
	mustAppend(t, p, hookEv("Stop", "new", "", ""), t0.Add(10*time.Second), "Mirasim")
	r, _ = h.Running(t0.Add(11 * time.Second))
	if since, ok := r.FlightSince(mira); ok {
		t.Fatalf("新会话 Stop 之后、老会话没有回合事件：不该在飞，实际起点 %v", since)
	}
	if !r.Rounds {
		t.Fatal("读到过回合事件，Rounds 为真")
	}
}

func hookEvTool(event, session, agent, toolUse, tool string) string {
	m := map[string]string{"hook_event_name": event, "session_id": session, "tool_name": tool}
	if agent != "" {
		m["agent_id"] = agent
	}
	if toolUse != "" {
		m["tool_use_id"] = toolUse
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// 主代理调 Agent 工具等子代理时，子代理在联网：Agent 不算本地忙，主代理也不算在飞；
// 子代理靠自己的 pre / post 判。Agent 的 post 回来后主代理接着在飞。
func TestHookAgentToolIsNotBusy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.jsonl")
	h := NewHook(p)
	t0 := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	mira := []string{"Mirasim"}
	mustAppend(t, p, hookEv("UserPromptSubmit", "s1", "", ""), t0, "Mirasim")
	mustAppend(t, p, hookEvTool("PreToolUse", "s1", "", "ag", "Agent"), t0.Add(2*time.Second), "Mirasim")
	r, _ := h.Running(t0.Add(3 * time.Second))
	if r.For(mira) {
		t.Fatal("Agent 工具不算本地忙")
	}
	if _, ok := r.FlightSince(mira); ok {
		t.Fatal("主代理在等子代理，不算主代理在飞（子代理自己判）")
	}
	// 子代理跑了一个工具，结束后子代理在飞
	mustAppend(t, p, hookEvTool("PreToolUse", "s1", "sub", "b", "Bash"), t0.Add(4*time.Second), "Mirasim")
	if r, _ := h.Running(t0.Add(5 * time.Second)); !r.For(mira) {
		t.Fatal("子代理的 Bash 算本地忙")
	}
	mustAppend(t, p, hookEvTool("PostToolUse", "s1", "sub", "b", "Bash"), t0.Add(6*time.Second), "Mirasim")
	r, _ = h.Running(t0.Add(7 * time.Second))
	if since, ok := r.FlightSince(mira); r.For(mira) || !ok || !since.Equal(t0.Add(6*time.Second)) {
		t.Fatalf("子代理工具结束：busy=%v 在飞起点 %v（%v）", r.For(mira), since, ok)
	}
	mustAppend(t, p, hookEv("SubagentStop", "s1", "sub", ""), t0.Add(8*time.Second), "Mirasim")
	mustAppend(t, p, hookEvTool("PostToolUse", "s1", "", "ag", "Agent"), t0.Add(9*time.Second), "Mirasim")
	r, _ = h.Running(t0.Add(10 * time.Second))
	if since, ok := r.FlightSince(mira); r.For(mira) || !ok || !since.Equal(t0.Add(9*time.Second)) {
		t.Fatalf("Agent 返回后主代理在飞：busy=%v 起点 %v（%v）", r.For(mira), since, ok)
	}
}
