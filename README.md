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

## Claude Code hook (optional)

Without the hook Fluxwatch still works; it just can't tell "a local tool is running" (BUSY) apart from a silent stream. With it, Claude Code calls `fluxwatch-cli hook` before and after every tool, and Fluxwatch appends one line (time, event, tool name, session id, and the app that started the session, found by walking up the parent processes — never tool input or output) to `~/.fluxwatch/hook.jsonl`. A running tool only marks the target whose app matches as BUSY (the mihomo source's `apps`, or the target name when `apps` is not set); lines with no app (older format, or `claude` started straight from a terminal) still apply to every target. The file is rotated to `hook.jsonl.1` when it passes 1 MB.

The rule (the settings window's "Claude Code hook" row checks exactly this): in `~/.claude/settings.json`, `hooks.PreToolUse` and `hooks.PostToolUse` each have an entry whose `matcher` is `*` (or empty) with a `"type": "command"` hook whose command runs `fluxwatch-cli hook`. The event is read from the JSON Claude Code passes on stdin, so no extra argument is needed.

If your `settings.json` already has a `hooks` section, add these entries to the existing `PreToolUse` / `PostToolUse` arrays instead of replacing them.

**macOS** — put `fluxwatch-cli` from the zip at `~/.fluxwatch/bin/fluxwatch-cli`, then clear the download quarantine so Claude Code can run it: `xattr -d com.apple.quarantine ~/.fluxwatch/bin/fluxwatch-cli`.

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "$HOME/.fluxwatch/bin/fluxwatch-cli hook" }] }
    ],
    "PostToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "$HOME/.fluxwatch/bin/fluxwatch-cli hook" }] }
    ]
  }
}
```

**Windows** — put `fluxwatch-cli.exe` at `C:\Users\<you>\.fluxwatch\bin\fluxwatch-cli.exe` and replace `<you>` below with your user name. Keep the forward slashes: if the hook runs through Git Bash, unquoted backslashes are eaten as escapes. (The Windows setup has not been verified on a real Windows machine yet.)

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe hook" }] }
    ],
    "PostToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe hook" }] }
    ]
  }
}
```

After restarting Claude Code, the settings window shows the hook as installed and, once a tool has run, the time of the last write to `hook.jsonl`.

## Development

Needs Go (version in `go.mod`) and Node. Run from the repo root:

- `pnpm dev` — run the CLI watcher in the foreground (`fluxwatch-cli watch`).
- `pnpm start` / `pnpm stop` — build and start the tray app in the background (pid in `_tmp/fluxwatch.pid`) / stop it.
- `pnpm check` — `gofmt` + `go vet` + `go test ./...` + build every `cmd/`. Run before pushing.
- `pnpm e2e` — end-to-end run of the real engine in a throwaway `HOME`: fake mihomo + fake hook events (busy / stall timing, busy only for the calling app, hook cost; this part also runs in CI), then, on your machine, a real headless Claude Code session running `sleep 70` with and without a session-scoped hook (`--skip-claude` to skip), and the tray app's stall notification on macOS (`--skip-tray` to skip). Never touches your `~/.fluxwatch` or `~/.claude/settings.json`. Logs and screenshots go to `_tmp/e2e/`.
