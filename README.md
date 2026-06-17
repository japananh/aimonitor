<div align="center">

# aimonitor

**Multi-account Claude Code usage monitor & silent account switcher for macOS & Linux.**

[![CI](https://github.com/japananh/aimonitor/actions/workflows/ci.yml/badge.svg)](https://github.com/japananh/aimonitor/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/japananh/aimonitor?sort=semver)](https://github.com/japananh/aimonitor/releases)

> **English** | [简体中文](README-zh.md) | [繁體中文](README-zh-TW.md) | [Tiếng Việt](README-vi.md)

<img src="docs/popover.png" alt="AIMonitor menu-bar popover: per-account 5h/7d usage bars" width="340">

</div>

## Features

- 🔍 **Live 5h + 7d usage bars per account** — polled from Anthropic's `/api/oauth/usage` (no tokens consumed), with a trend line (`↗ +21% in 45m`).
- 🔀 **Silent switching** — `aimonitor switch <label>` refreshes the OAuth token and swaps the live credential. No `claude /login`, no terminal hop.
- 🤖 **Auto-swap** at the 5h *or* 7d threshold (default 80 %) — picks the account with the most overall headroom, skips exhausted/rate-limited ones, and rescues immediately if the active account hits 100 %. Running `claude` sessions follow automatically.
- 🔔 **Threshold notifications** as an account nears its limit (when auto-swap is off).
- 💾 **Export / import** settings, or migrate accounts to another machine — credentials optional and passphrase-encrypted (Argon2id + AES-256-GCM).
- 🔌 **MCP server** — 30 Slack + ClickUp tools for Claude Code over stdio, with per-service read-only mode.
- 🔐 **OS-keyring storage** (macOS Keychain, Linux libsecret). SQLite holds references; tokens never leave the keyring. No telemetry.

## Install

```sh
# macOS (Sonoma 14+) — one command: taps, trusts, installs, clears Gatekeeper
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/macos/install.sh | bash

# Linux (Ubuntu 22.04+) — CLI only
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh

# Any platform, CLI only
go install github.com/japananh/aimonitor/cmd/aimonitor@latest
```

> **Prefer Homebrew directly?** `brew trust japananh/tap && brew install --cask japananh/tap/aimonitor`, then clear Gatekeeper on first launch: `xattr -dr com.apple.quarantine /Applications/AIMonitor.app` (or right-click → Open). The one-line installer above does both for you. See [`docs/unsigned-app.md`](docs/unsigned-app.md).

### Upgrade

```sh
brew upgrade --cask aimonitor   # macOS
aimonitor update check          # CLI: is a newer release out?
aimonitor update install        # CLI: upgrade in the background
```

The menu-bar app also checks GitHub on launch and offers the update under **Preferences → Check for updates**. Pre-releases are never auto-served — `brew upgrade` keeps you on the latest stable.

## Quick start

```sh
aimonitor add --adopt-current --label personal   # register your current Claude login
aimonitor add --label work                        # add another (drives claude /login, polls keychain)
aimonitor switch work                             # switch silently
aimonitor list                                    # live 5h / 7d usage per account
aimonitor doctor                                  # health check
```

Already on another switcher? `aimonitor import` pulls its accounts in one step. Auto-swap is on by default at 80 % — nothing else to configure for the common case.

## Configuration

```sh
aimonitor config set auto_swap.enabled true        # default true
aimonitor config set auto_swap.threshold_pct 80    # 5h threshold
aimonitor config set auto_swap.threshold_7d_pct 80 # 7d threshold
aimonitor config set autostart true                # daemon at login
```

Back up or move to another machine:

```sh
aimonitor config export --out backup.json                                          # settings only (no secrets)
AIMONITOR_PASSPHRASE=… aimonitor config export --include-tokens --out full.json     # + encrypted credentials
AIMONITOR_PASSPHRASE=… aimonitor config import full.json                            # restore elsewhere
```

`--include-tokens` bundles your logins encrypted under the passphrase — restoring it means `claude` works on the other machine without re-login, so treat that file like a password. Same actions live in Preferences → Backup.

<details>
<summary><b>All config keys</b></summary>

| Key | Default | Description |
|---|---|---|
| `auto_swap.enabled` | `true` | Master toggle for auto-swap |
| `auto_swap.threshold_pct` | `80` | 5h utilization (%) to auto-swap |
| `auto_swap.threshold_7d_pct` | `80` | 7d utilization (%) to auto-swap |
| `auto_swap.grace_sec` | `60` | Delay between the "pending" notification and the swap (`0` = immediate) |
| `notify.enabled` | `true` | Warn as the active account nears its limit (only when auto-swap is off) |
| `notify.warn_pct` / `notify.crit_pct` | `80` / `95` | Warning / critical notification levels |
| `auto_update.enabled` | `true` | Check GitHub for releases on launch (never auto-installs) |
| `autostart` | `false` | Start the daemon at login |
| `mcp.slack.enabled` / `mcp.clickup.enabled` | `true` | Expose that service's MCP tools |
| `mcp.slack.read_only` / `mcp.clickup.read_only` | `false` | Hide the service's write tools |
| `mcp.disabled_tools` | (empty) | Comma-separated tool names to hide |

</details>

## How it works

The daemon polls `/api/oauth/usage` (~5 min ± jitter, no tokens consumed). When the active account crosses its 5h **or** 7d threshold, it picks the account with the most overall headroom, refreshes that account's OAuth token (`POST .../v1/oauth/token`), and writes it to the live Keychain slot. Running and new `claude` sessions adopt the new account — no `/login`, no restart.

See [`docs/architecture.md`](docs/architecture.md) and [`docs/thresholds.md`](docs/thresholds.md) for the full picture.

## MCP server (Slack + ClickUp for Claude Code)

One stdio process serving 30 tools — no extra runtimes.

```sh
aimonitor mcp connect slack     # store a Slack user token (xoxp-…)
aimonitor mcp connect clickup   # store a ClickUp token (pk_…)
aimonitor mcp register          # add the server to Claude Code
```

- **Slack:** post to channels/threads (mrkdwn, code blocks), upload, search, history, permalinks.
- **ClickUp:** workspace hierarchy, tasks, comments, Docs (read & write).
- **Safety:** Claude Code's per-tool prompts are the approval layer; per-service Enabled / Read-only switches and a per-tool disable list refine it. Tokens are verified live, then stored in the OS keyring — never in SQLite or logs.

> **Slack token scopes.** The Slack token is a **user** token (`xoxp-…`). Grant these **User Token Scopes** on your Slack app (api.slack.com → OAuth & Permissions), reinstall, then connect — a missing one surfaces as `slack: missing scope "…"` on the affected tool:
> `search:read`, `users:read`, `channels:history`, `groups:history`, `im:history`, `mpim:history`, `channels:read`, `groups:read`, `im:read`, `mpim:read`, `chat:write`, `files:write`.

## Privacy & security

- No telemetry, no phone-home. OAuth tokens live only in the OS keyring; SQLite holds references. Token bytes are never logged.
- Outbound traffic is limited to: `GET /api/oauth/usage` (introspection, no tokens consumed), `POST /v1/oauth/token` (silent token refresh), and the GitHub release check. Nothing about you is sent.

See [`docs/security.md`](docs/security.md) for the threat model.

## Troubleshooting

```sh
aimonitor doctor   # health check: config, SQLite, keyring, accounts
```

- **"Daemon not running" / usage looks stale.** Start or restart the background daemon with `aimonitor config set autostart true`, or click **Start daemon** in the popover — it registers a LaunchAgent that relaunches at login.
- **App won't open on first launch** (unsigned). Clear Gatekeeper once: `xattr -dr com.apple.quarantine /Applications/AIMonitor.app`.
- **Logs.** The daemon writes to `~/Library/Logs/aimonitor/aimonitor.daemon.log` (INFO/WARN/ERROR — never token bytes); background upgrades log to `update.log` beside it.
- **Recent switches.** `aimonitor log` prints the switch audit trail.

## Uninstall

```sh
# Remove the app + daemon, keep your accounts
brew uninstall --cask aimonitor

# Full wipe, including the logins saved in your Keychain. Purge runs first
# because Homebrew can't reach the keychain stashes — and it needs the
# binary still installed to clear them.
aimonitor uninstall --purge && brew uninstall --cask aimonitor
```

`--purge` only deletes **aimonitor's own** Keychain entries (the `aimonitor-<uuid>` stashes), never Claude's `Claude Code-credentials` slot. So the account you're currently signed into keeps working in `claude` with **no re-login** — only aimonitor's saved copies of your *other* accounts are dropped.

## Build from source

Requires Go 1.25+. Pure Go (`CGO_ENABLED=0` works on macOS; keychain access shells out to `/usr/bin/security`).

```sh
make build              # CLI binary
make test               # unit tests
make widget             # AIMonitor.app (macOS; needs the Swift toolchain)
make release-snapshot   # goreleaser dry-run
```

## License

[MIT](LICENSE) © [@japananh](https://github.com/japananh)
