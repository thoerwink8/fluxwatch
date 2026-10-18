# AGENTS.md

<!-- repo-kind: 私人仓 -->

本仓特有的规矩（通用规矩以 fleet-dao 为准）。

Fluxwatch：菜单栏 / 托盘小工具，盯 reclaude、mirasim 这类 AI 流量，卡住时让人看得见、并指出卡在哪一层。只观测，不切节点、不改代理配置。

## 文档在哪
- 目标和范围：`docs/goals.md`；设计（数据源、判定、界面）：`docs/design.md`；界面草图：`docs/design/ui-sketch.html`。
- 决定记录：`docs/decisions/`；进度、创始人引导、临时调整：`docs/PROGRESS.md`。
- 需求照 fleet-dao 的做法管在本仓的 GitHub issue 上：创始人在对话里说，AI 当场开单，正文四节「场景、原话、已知的模块、怎么算做完」，原话贴他亲口说的；一个 PR 当轮做得完的不开单直接做。要写方案才建 `specs/<单号>-<短名>/`。
- 以后导入驾驶舱也是读这些单，不另起一套；界面类的单（要在 mac 上真跑、截图）标「本机做」，法国的引擎接不了。

## 代码
- Go，一个模块。`internal/model` 是引擎和界面之间的约定，改字段两边一起改。
- 引擎和数据源（`internal/` 除 `internal/app`）不用 cgo，能在 Linux 上测；平台相关的代码按 `_darwin.go` / `_windows.go` 分文件，另一平台给能编译的空实现。
- 界面是 `internal/app` + `frontend/`（Wails v3，纯 HTML/CSS/JS，不上前端框架），照 `docs/design/ui-sketch.html` 做，明暗两套主题。
- 读不到数据源时状态写 `unknown` 并写明原因，不当成正常。
- 不记、不传会话内容：只碰连接字节数、域名、节点名、进程名、日志里的状态码。

## 命令
- `pnpm dev`：命令行观测器前台跑（`fluxwatch-cli watch`）。`pnpm start` / `pnpm stop`：起停托盘应用。`pnpm check`：`go vet` + `go test ./...` + 构建。
- 推之前跑 `pnpm check`。CI 在 macOS 和 Windows 上各跑一遍，碰界面的 PR 带截图。

## 公开仓
不提交订阅链接、节点密钥、本机路径、私有域名。示例数据用 `example.com` 一类。
