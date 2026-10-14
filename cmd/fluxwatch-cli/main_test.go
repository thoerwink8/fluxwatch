package main

import (
	"strings"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

func TestChangesPrintsOnlyTransitions(t *testing.T) {
	prev, prevErr := map[string]model.State{}, map[string]bool{}
	at := time.Date(2026, 10, 14, 10, 0, 1, 0, time.Local)
	s := model.Snapshot{At: at, Targets: []model.Target{{Name: "reclaude", State: model.StateIdle}}}
	if got := changes(s, prev, prevErr); len(got) != 1 || !strings.Contains(got[0], "—→idle") {
		t.Fatalf("首行：%v", got)
	}
	if got := changes(s, prev, prevErr); len(got) != 0 {
		t.Fatalf("没变化不该打印：%v", got)
	}
	s.Targets[0] = model.Target{Name: "reclaude", State: model.StateStall, Layer: model.LayerLine, Reason: "节点 JP-1 字节停了 30s", Node: "JP-1"}
	s.SourceErr = []string{"hook：x"}
	got := changes(s, prev, prevErr)
	if len(got) != 2 || got[0] != "10:00:01  reclaude     idle→stall  A  节点 JP-1 字节停了 30s  JP-1" || !strings.Contains(got[1], "数据源问题") {
		t.Fatalf("变化行：%q", got)
	}
	s.SourceErr = nil
	s.Targets = nil
	got = changes(s, prev, prevErr)
	if len(got) != 2 {
		t.Fatalf("目标删除、问题恢复：%q", got)
	}
}
