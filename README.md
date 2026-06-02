<div align="center">

# aimonitor

**Multi-account Claude Code session monitor and silent account switcher for macOS & Linux.**

[![CI](https://github.com/japananh/aimonitor/actions/workflows/ci.yml/badge.svg)](https://github.com/japananh/aimonitor/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/japananh/aimonitor?include_prereleases&sort=semver)](https://github.com/japananh/aimonitor/releases)

</div>

<p align="center">
  <!-- TODO: replace src with the real path once the PNG is committed (e.g. docs/screenshot.png) -->
  <img src="docs/screenshot.png" alt="aimonitor menu bar popover with 5-hour and 7-day usage bars" width="420" />
</p>

## Features

- 🔍 **Live 5-hour and 7-day usage bars** in your menu bar — server-side truth, polled from Anthropic's `/api/oauth/usage` introspection endpoint. No tokens consumed.
- 🔀 **Silent account switching** — `aimonitor switch <label>` refreshes the OAuth access token via Anthropic's token endpoint and writes the live credential. No terminal hop, no `claude /login`.
- 🤖 **Auto-swap** at 80 % utilization (configurable). Picks the account with the most headroom; optionally SIGINTs running `claude` REPLs so they restart against the new credential.
- 🔐 **OS-keyring credential storage** (macOS Keychain via `/usr/bin/security`, Linux libsecret). SQLite holds references; tokens never leave the keyring.
- 📡 **Local-first.** No telemetry. No phone-home.

## Install

### macOS (Sonoma 14+)

```sh
brew install --cask japananh/tap/aimonitor
```

> **First launch:** the `.app` is unsigned in v1.0.0-beta (notarization is a v1.1 deliverable). Clear the Gatekeeper quarantine once:
> ```sh
> xattr -dr com.apple.quarantine /Applications/AIMonitor.app
> ```
> Or right-click → Open → confirm. See [`docs/unsigned-app.md`](docs/unsigned-app.md).

### Linux (Ubuntu 22.04+)

```sh
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh
```

CLI only on Linux; the GTK menu bar widget is a v2.0 deliverable.

### Via `go install` (CLI only, any platform)

```sh
go install github.com/japananh/aimonitor/cmd/aimonitor@latest
```

Lands `aimonitor` in `$GOBIN`. No `.app`, no autostart service — useful if you only need the CLI for switching accounts from a terminal, or you don't want to add a Homebrew tap.

## Quick start

```sh
# 1. Register your current Claude Code login as the first aimonitor account.
#    --adopt-current adopts the credential already in your keychain instead
#    of driving a fresh OAuth flow.
aimonitor add --adopt-current --label personal

# 2. Add a second account. aimonitor stashes the current credential, prints
#    instructions, polls the keychain. You drive `claude` + `/login` in
#    another terminal.
aimonitor add --label work

# 3. Switch silently — no terminal, no /login.
aimonitor switch work

# 4. See live 5h / 7d usage per account.
aimonitor list

# 5. Health check.
aimonitor doctor
```

Auto-swap is on by default at 80 % 5-hour utilization. Nothing else to configure for the common case.

## Configuration

```sh
aimonitor config set auto_swap.enabled true       # default true
aimonitor config set auto_swap.threshold_pct 80   # default 80
aimonitor config set autostart true                # daemon at login
```

<details>
<summary><b>Full config keys</b></summary>

| Key | Default | Description |
|---|---|---|
| `auto_swap.enabled` | `true` | Master toggle for the OAuth-limits-driven auto-swap |
| `auto_swap.threshold_pct` | `80` | 5-hour utilization (%) at which to auto-swap |
| `auto_swap.grace_sec` | `60` | Seconds between the "auto-swap pending" notification and the actual swap, so you can wrap up a live `claude` session. `0` swaps immediately. |
| `autostart` | `false` | Start the daemon at login |
| `autoswitch` | `false` | (Legacy) tripwire-driven JSONL accumulator. Disabled in v1.0.0-beta.4 — the new `auto_swap.*` keys supersede it. |

</details>

## How it works

When the active account hits the configured 5-hour utilization threshold, aimonitor finds the next-lowest-utilization account and silently swaps:

```
                      polled every 5 min ± 30 s jitter
                ┌─────────────────────────────────────────┐
                │  GET  api.anthropic.com/api/oauth/usage │
                │       → 5h % + 7d % + reset times       │
                └─────────────────────────────────────────┘
                                   │
              5h utilization ≥ threshold?
                          │
                          ▼  yes — pick lowest-utilization account
   ┌──────────────────┐   POST platform.claude.com/v1/oauth/token
   │ target account   │ ──────────────────────────────────────────▶
   │ refresh_token    │   grant_type=refresh_token
   └──────────────────┘                  │
                                         ▼ fresh access_token
                          ┌───────────────────────────┐
                          │ Claude Code-credentials   │
                          │   (macOS Keychain slot)   │
                          └───────────────────────────┘
                                         │
                                         ▼
                            next `claude` invocation
                            uses the new account
                            — no /login required
```

See [`docs/architecture.md`](docs/architecture.md) for the full daemon / store / widget breakdown.

## Privacy & security

- **No telemetry. No phone-home.** Anywhere.
- OAuth tokens live only in the OS keyring. SQLite holds references, never secrets.
- Token bytes are never logged, even at `--debug` level. Log scrubbing matches `sk-ant-(oat|ort)…`.
- **The only outbound traffic** aimonitor initiates is to two Anthropic OAuth surfaces:
  - `GET https://api.anthropic.com/api/oauth/usage` — introspection-only, ~5 KB per call. Consumes no tokens. Background interval: 5 min ± 30 s jitter for the active account, with exponential backoff on errors (capped at 1 h). Inactive accounts are fetched only when you open the popover.
  - `POST https://platform.claude.com/v1/oauth/token` — only on account switches when the cached access token is near or past expiry. Silent (no browser).
- The legacy `aimonitor probe` CLI subcommand fires a real `/v1/messages` request and is deprecated. The daemon no longer uses it.

See [`docs/security.md`](docs/security.md) for the full threat model.

## Roadmap

Directional, not committed.

- **v1.1:** daily usage chart, cost estimation per account, notarized macOS app.
- **v1.2 (contingent on v1.1 notarization):** submit to `homebrew/cask` so `brew install aimonitor` works without tapping a third-party repo.
- **v2.0:** Ubuntu GTK menu bar widget, second `Provider` implementation (Codex or Copilot CLI).

## Uninstall

```sh
aimonitor uninstall              # disable autostart; keep your data
aimonitor uninstall --purge      # also drop SQLite DB, config, aimonitor keyring entries

# macOS
brew uninstall --cask aimonitor
brew untap japananh/tap          # optional

# Linux
systemctl --user disable aimonitor.service
sudo rm /usr/local/bin/aimonitor
```

Your original `Claude Code-credentials` keyring entry is **never touched** by aimonitor's uninstall — existing `claude` CLI logins keep working.

## Build from source

Requires Go 1.25+. Pure Go on all platforms — `CGO_ENABLED=0` works on macOS too since v1.0.0-beta.4 (keychain access shells out to `/usr/bin/security` instead of linking the Security framework via cgo).

```sh
git clone https://github.com/japananh/aimonitor
cd aimonitor
make build              # Go CLI binary
make test               # unit tests
make widget             # AIMonitor.app via Swift Package Manager (macOS only)
make release-snapshot   # full goreleaser dry-run (no publish; needs goreleaser installed)
```

On macOS the menu bar widget needs the Swift toolchain (`xcode-select --install`). Full Xcode is not required; the widget builds headlessly via Swift Package Manager.

## Documentation

| Topic | Where |
|---|---|
| Architecture (daemon, store, widget) | [`docs/architecture.md`](docs/architecture.md) |
| Threat model + scrubbing rules | [`docs/security.md`](docs/security.md) |
| Why the macOS `.app` is unsigned in v1.0.0-beta | [`docs/unsigned-app.md`](docs/unsigned-app.md) |
| User stories shipped in v1 | [`USER_STORIES.md`](USER_STORIES.md) |

## See also

Related tools in the Claude-Code-ergonomics space:

- [ncthanhngo/claude-bar](https://github.com/ncthanhngo/claude-bar) — sibling macOS menu-bar app and the source of patterns aimonitor learned from (keychain shell-out, OAuth refresh flow, post-swap SIGINT).
- [ryoppippi/ccusage](https://github.com/ryoppippi/ccusage) — pure-CLI usage analyzer; parses local Claude transcripts for per-day / per-session breakdowns. Read-only, no credential management.

## License

MIT. See [`LICENSE`](LICENSE).
