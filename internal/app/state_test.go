package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/model"
)

func TestStateWriterAtomicAndReadable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".fluxwatch", stateFile)
	w := newStateWriter(p)
	at := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return at }
	w.write(snap(model.StateStall, model.StateUnknown))
	w.write(snap(model.StateOK)) // 第二次覆盖第一次

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		WrittenAt time.Time      `json:"writtenAt"`
		PID       int            `json:"pid"`
		Snapshot  model.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("不是合法 JSON：%v\n%s", err, b)
	}
	if !d.WrittenAt.Equal(at) || d.PID != os.Getpid() || len(d.Snapshot.Targets) != 1 || d.Snapshot.Targets[0].State != model.StateOK {
		t.Fatalf("内容 = %+v", d)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("临时文件没收拾干净：%v", ents)
	}
}
