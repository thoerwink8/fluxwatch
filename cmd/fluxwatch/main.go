// fluxwatch 是托盘应用：mac 菜单栏 / Windows 托盘。`-fake` 用演示数据跑。
package main

import (
	"context"
	"flag"
	"log"

	"github.com/thoerwink8/fluxwatch/frontend"
	"github.com/thoerwink8/fluxwatch/internal/app"
	"github.com/thoerwink8/fluxwatch/internal/app/fake"
	"github.com/thoerwink8/fluxwatch/internal/engine"
	"github.com/thoerwink8/fluxwatch/internal/model"
)

// version 发版时由 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	useFake := flag.Bool("fake", false, "用演示数据（卡住、慢、本地忙、上游 503）代替真引擎")
	shots := flag.String("shots", "", "截图模式：拍面板、发现窗、设置窗和托盘图标到这个目录后退出（mac，-tags shots 构建）")
	flag.Parse()

	var p model.Provider
	stop := func() {}
	if *useFake {
		p = fake.New()
	} else {
		p, stop = newEngine()
	}
	err := app.Run(app.Options{Provider: p, Assets: frontend.Assets, Fake: *useFake, Version: version, ShotsDir: *shots})
	stop() // 引擎收尾：把还没恢复的卡住写进账本
	if err != nil {
		log.Fatal(err)
	}
}

// newEngine 起真引擎（每秒一轮）；第二个返回值停掉它并等收尾。
// 引擎起不来（比如 targets.yaml 写坏了）时不假装有数据：整体报 unknown 并照实写明原因，
// 每 5 秒重试一次，改好后自动起来，不用重启托盘。
func newEngine() (model.Provider, func()) {
	start := func() (runner, error) { return engine.New(engine.Options{}) }
	ctx, cancel := context.WithCancel(context.Background())
	e, err := start()
	if err != nil {
		log.Printf("引擎起不来，%s 后重试：%v", retryEvery, err)
		h := newHealing(start, retryEvery, err)
		done := make(chan struct{})
		go func() { h.run(ctx); close(done) }()
		return h, func() { cancel(); <-done; h.wait() }
	}
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	return e, func() { cancel(); <-done }
}
