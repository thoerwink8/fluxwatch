# Fluxwatch

A menu bar / tray app for macOS and Windows that watches AI traffic (Claude Code via reclaude, Mirasim Cloud) through your Clash / mihomo proxy, and tells you when a stream has silently stalled — and which layer it stalled in: the proxy line, the upstream service, the account quota, or nothing at all because a local tool is running.

Observe only: it never switches nodes or touches your proxy config.

- Design: [`docs/design.md`](docs/design.md) · UI sketch: [`docs/design/ui-sketch.html`](docs/design/ui-sketch.html)
- 中文说明见 [`docs/goals.md`](docs/goals.md)

Status: in development.

## Install

Download the zip for your platform from [Releases](https://github.com/thoerwink8/fluxwatch/releases) (check it against `SHA256SUMS.txt` on the same page: `shasum -a 256 -c SHA256SUMS.txt --ignore-missing`):

- macOS (Apple Silicon / Intel): unzip, move `Fluxwatch.app` to Applications. The app is ad-hoc signed but **not notarized** (notarization needs a paid Apple Developer account; that is a decision, not an oversight), so Gatekeeper blocks the first launch. Either open **System Settings → Privacy & Security**, scroll down and click **Open Anyway** next to the Fluxwatch message (then confirm), or run `xattr -dr com.apple.quarantine /Applications/Fluxwatch.app` once. (Right-click → Open no longer bypasses this on macOS 15 and later.) It lives in the menu bar, with no Dock icon.
- Windows (x64): unzip, run `fluxwatch.exe` (tray app). The exe is not code-signed, so SmartScreen may show "Windows protected your PC": click **More info → Run anyway**. `fluxwatch-cli.exe` is the command-line watcher. Needs the WebView2 runtime (preinstalled on Windows 11).

## Claude Code hook (optional)

Without the hook Fluxwatch still works; it just can't tell "a local tool is running" (BUSY) apart from a silent stream. With it, Claude Code calls `fluxwatch-cli hook` around every tool call and at the start and end of every turn, and Fluxwatch appends one line to `~/.fluxwatch/hook.jsonl`: time, event, tool name, session id, subagent id, tool-use id, and the app that started the session (found by walking up the parent processes). Never tool input or output, never the prompt. The file is rotated to `hook.jsonl.1` when it passes 1 MB.

What each event is for:

| Claude Code event | Line in `hook.jsonl` | Used for |
|---|---|---|
| `PreToolUse` | `pre` | a tool call started: its app is BUSY |
| `PostToolUse`, `PostToolUseFailure` | `post` | that call ended (paired by `tool_use_id`) |
| `PermissionDenied` | `denied` | the call was refused, so no `post` will come |
| `UserPromptSubmit` | `prompt` | a turn started: waiting for the model |
| `Stop`, `StopFailure` | `stop`, `stop_failure` | the turn ended: every call still open in that session is closed |
| `SubagentStop` | `subagent_stop` | a subagent ended: its open calls are closed |
| `SessionEnd` | `session_end` | the session ended: everything in it is closed |

Each tool call is paired with its own `post` by `tool_use_id` (older lines without it are paired first-in, first-out per session) and counts as running for at most 10 minutes, so one lost `post` cannot keep a target BUSY. A running tool only marks the target whose app matches as BUSY (the mihomo source's `apps`, or the target name when `apps` is not set); lines with no app (older format, or `claude` started straight from a terminal) still apply to every target. With only `PreToolUse` / `PostToolUse` installed BUSY still works, but a failed tool call (Claude Code reports it as `PostToolUseFailure`, not `PostToolUse`) then stays BUSY for the full 10 minutes; the turn events make it end promptly at `Stop` and let Fluxwatch tell "waiting for the model" from "idle".

The hook never gets in Claude Code's way: `fluxwatch-cli hook` always exits 0 and writes any error to `~/.fluxwatch/hook.err` (kept under 64 KB, rotated to `hook.err.1`), and the commands below check that the binary exists first, so moving or deleting it does not make every tool call show a hook error.

The rule (the settings window's "Claude Code hook" row checks exactly this): in `~/.claude/settings.json`, `hooks.PreToolUse` and `hooks.PostToolUse` each have an entry whose `matcher` is `*` (or empty) with a `"type": "command"` hook whose command runs `fluxwatch-cli hook`; that is enough to count as installed. The settings window also lists any of `PostToolUseFailure`, `UserPromptSubmit`, `Stop`, `StopFailure`, `SubagentStop`, `SessionEnd`, `PermissionDenied` that are missing, and checks that the binary the command points at exists. The event is read from the JSON Claude Code passes on stdin, so no extra argument is needed.

If your `settings.json` already has a `hooks` section, add these entries to the existing arrays instead of replacing them; if an older Fluxwatch entry (`.../fluxwatch-cli hook` without the `[ -x ... ]` check) is there, replace it.

**macOS** — put `fluxwatch-cli` from the zip at `~/.fluxwatch/bin/fluxwatch-cli`, then clear the download quarantine so Claude Code can run it: `xattr -d com.apple.quarantine ~/.fluxwatch/bin/fluxwatch-cli`.

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ],
    "PostToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ],
    "PostToolUseFailure": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ],
    "PermissionDenied": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ],
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ],
    "Stop": [
      { "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ],
    "StopFailure": [
      { "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ],
    "SubagentStop": [
      { "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ],
    "SessionEnd": [
      { "hooks": [{ "type": "command", "command": "[ -x \"$HOME/.fluxwatch/bin/fluxwatch-cli\" ] && \"$HOME/.fluxwatch/bin/fluxwatch-cli\" hook || true" }] }
    ]
  }
}
```

**Windows** — put `fluxwatch-cli.exe` at `C:\Users\<you>\.fluxwatch\bin\fluxwatch-cli.exe` and replace `<you>` below with your user name. Claude Code runs hook commands through Git Bash on Windows, so the command uses the same `[ -x ... ] && ... || true` check as on macOS; keep the forward slashes (unquoted backslashes are eaten as escapes). If Git Bash is not installed, Claude Code falls back to PowerShell: use `if (Test-Path 'C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe') { & 'C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe' hook }` as the command instead. (The Windows setup has not been verified on a real Windows machine yet.)

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
    ],
    "PostToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
    ],
    "PostToolUseFailure": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
    ],
    "PermissionDenied": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
    ],
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
    ],
    "Stop": [
      { "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
    ],
    "StopFailure": [
      { "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
    ],
    "SubagentStop": [
      { "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
    ],
    "SessionEnd": [
      { "hooks": [{ "type": "command", "command": "[ -x \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" ] && \"C:/Users/<you>/.fluxwatch/bin/fluxwatch-cli.exe\" hook || true" }] }
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
