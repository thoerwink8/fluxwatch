// fluxwatch 是托盘应用：mac 菜单栏 / Windows 托盘。`-fake` 用演示数据跑。
package main

import (
	"flag"
	"log"
	"sync"
	"time"

	"github.com/thoerwink8/fluxwatch/frontend"
	"github.com/thoerwink8/fluxwatch/internal/app"
	"github.com/thoerwink8/fluxwatch/internal/app/fake"
	"github.com/thoerwink8/fluxwatch/internal/model"
)

// version 发版时由 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	useFake := flag.Bool("fake", false, "用演示数据（卡住、慢、本地忙、上游 503）代替真引擎")
	shots := flag.String("shots", "", "截图模式：拍面板、发现窗、设置窗和托盘图标到这个目录后退出（mac，-tags shots 构建）")
	flag.Parse()

	var p model.Provider
	if *useFake {
		p = fake.New()
	} else {
		p = newEngine()
	}
	if err := app.Run(app.Options{Provider: p, Assets: frontend.Assets, Fake: *useFake, Version: version, ShotsDir: *shots}); err != nil {
		log.Fatal(err)
	}
}

// newEngine 是接真引擎的地方。
//
// TODO(#1)：internal/engine 合进 main 后换成 engine.New(...)（它实现 model.Provider），删掉 noEngine。
// 在那之前不假装有数据：整体状态报 unknown，并照实写明原因。
func newEngine() model.Provider { return &noEngine{} }

type noEngine struct{}

const noEngineReason = "引擎还没接上（#1 合进来之前只能用 -fake 看演示）"

func (n *noEngine) Snapshot() model.Snapshot {
	return model.Snapshot{At: time.Now(), Worst: model.StateUnknown, SourceErr: []string{noEngineReason}}
}

func (n *noEngine) Subscribe() (<-chan model.Snapshot, func()) {
	ch := make(chan model.Snapshot, 1)
	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				select {
				case ch <- n.Snapshot():
				default:
				}
			}
		}
	}()
	var once sync.Once
	return ch, func() { once.Do(func() { close(stop) }) }
}

func (n *noEngine) Discover() ([]model.Discovered, error) {
	return nil, errNoEngine
}

func (n *noEngine) AddTarget(string, string) error { return errNoEngine }
func (n *noEngine) RemoveTarget(string) error      { return errNoEngine }
func (n *noEngine) Diagnose(string) string         { return noEngineReason }

type engineErr string

func (e engineErr) Error() string { return string(e) }

const errNoEngine = engineErr(noEngineReason)
