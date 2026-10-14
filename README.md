# Fluxwatch

A menu bar / tray app for macOS and Windows that watches AI traffic (Claude Code via reclaude, Mirasim Cloud) through your Clash / mihomo proxy, and tells you when a stream has silently stalled — and which layer it stalled in: the proxy line, the upstream service, the account quota, or nothing at all because a local tool is running.

Observe only: it never switches nodes or touches your proxy config.

- Design: [`docs/design.md`](docs/design.md) · UI sketch: [`docs/design/ui-sketch.html`](docs/design/ui-sketch.html)
- 中文说明见 [`docs/goals.md`](docs/goals.md)

Status: in development.

## Install

Download the zip for your platform from [Releases](https://github.com/thoerwink8/fluxwatch/releases):

- macOS (Apple Silicon / Intel): unzip, move `Fluxwatch.app` to Applications. The app is not signed yet, so the first time, **right-click it and choose Open** (then Open again in the dialog). A plain double-click is blocked by Gatekeeper. It lives in the menu bar, with no Dock icon.
- Windows (x64): unzip, run `fluxwatch.exe` (tray app). `fluxwatch-cli.exe` is the command-line watcher. Needs the WebView2 runtime (preinstalled on Windows 11).

## Development

Needs Go (version in `go.mod`) and Node. Run from the repo root:

- `pnpm dev` — run the CLI watcher in the foreground (`fluxwatch-cli watch`).
- `pnpm start` / `pnpm stop` — build and start the tray app in the background (pid in `_tmp/fluxwatch.pid`) / stop it.
- `pnpm check` — `gofmt` + `go vet` + `go test ./...` + build every `cmd/`. Run before pushing.
