# 0001 只观测；Go + Wails v3；目标靠发现面板加

日期：2026-10-14　拍板：创始人

## 定了什么
1. 名字 **Fluxwatch**（创始人：「Fluxwatch」）。flux 同时是流量和变化，watch 是监控。
2. **只观测。** 不自动切节点、不改代理配置（创始人：「我自己手动 clash 切节点，工具只需要能观测到就行」）。界面只留「复制诊断」「打开日志」。
3. **先盯 reclaude 和 mirasim，其余从正在跑的流量里挑。** 发现面板按域名列出此刻的连接，一键加入监控（创始人：「目标目前只接 reclaude 和 mirasim……想办法能监测正在运行的流量，可以加入观测」）。
4. **每条流量标出软件名**（创始人：「把调动该进程的软件名也加进去」）。mihomo 拿不到进程，按本地端口反查、上溯到软件。
5. **Go**，不用 Rust。这类工具的成败在阈值，阈值要反复试，编译快比运行时省几 MB 内存要紧。
6. **界面用 Wails v3**（钉在 v3.0.0-beta.29）。它自带托盘和贴着托盘弹出的窗口，面板直接用 HTML/CSS 照草图做。Wails v3 还是 beta：引擎和界面隔在 `internal/model.Provider` 后面，beta 出问题换 fyne.io/systray + webview 只动 `internal/app`。
7. 公开仓 `thoerwink8/fluxwatch`，构建走 GitHub Actions，mac、Windows 各自原生编。

## 为什么不先只做命令行
早先方案是先做命令行跑三天验误报率再做界面。创始人要直接实现；命令行观测器照样做（`fluxwatch-cli watch`），和界面共用一个引擎，账本照记，误报率照样能从账本里看。
