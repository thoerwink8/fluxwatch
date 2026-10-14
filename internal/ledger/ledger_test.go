package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTodayCountByLocalDateTargetNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.Local)
	l, err := Open(path, now)
	if err != nil {
		t.Fatal(err)
	}
	add := func(at time.Time, target, node string) {
		t.Helper()
		if err := l.Append(Entry{At: at, Target: target, Layer: "A", Node: node, Seconds: 31}); err != nil {
			t.Fatal(err)
		}
	}
	add(now.Add(-2*time.Hour), "reclaude", "JP-1")
	add(now.Add(-1*time.Hour), "reclaude", "JP-1")
	add(now.Add(-30*time.Minute), "reclaude", "US-2")
	add(now.Add(-10*time.Minute), "mirasim", "JP-1")
	add(time.Date(2026, 10, 13, 23, 59, 0, 0, time.Local), "reclaude", "JP-1") // 昨天

	if got := l.TodayCount(now, "reclaude", "JP-1"); got != 2 {
		t.Fatalf("reclaude@JP-1 今天 = %d，要 2", got)
	}
	if got := l.TodayCount(now, "reclaude", ""); got != 3 {
		t.Fatalf("reclaude 不分节点 = %d，要 3", got)
	}

	// 重新打开从文件里算出同样的数
	l2, err := Open(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := l2.TodayCount(now, "reclaude", "JP-1"); got != 2 {
		t.Fatalf("重开后 = %d，要 2", got)
	}
}

func TestOpenSkipsBadLineButReportsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	now := time.Now()
	good := `{"at":"` + now.Format(time.RFC3339) + `","target":"x","layer":"B","node":"n","seconds":40}`
	if err := os.WriteFile(path, []byte("not json\n"+good+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, now)
	if err == nil {
		t.Fatal("坏行应当报出来")
	}
	if l == nil || l.TodayCount(now, "x", "n") != 1 {
		t.Fatal("好行应当照算")
	}
}
