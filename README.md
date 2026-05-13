# aimonitor

Multi-account Claude Code session monitor and account switcher for macOS & Linux.

> **Status**: v1.0.0-beta (in development).
> **Repository**: <https://github.com/japananh/aimonitor>

`aimonitor` solves one problem: when several teammates share Anthropic Claude Team seats, one heavy user can drain a session quota and block everyone else. `aimonitor` lets you (1) see live session usage in your macOS menu bar, (2) keep multiple Claude OAuth credentials stashed in your OS keyring, and (3) switch — manually or automatically — to whichever account still has server-side headroom.

It is local-first. It has no telemetry. It never phones home.

## Why "server-side headroom"?

Claude Code's local JSONL transcripts only record tokens used on **this** machine. The Anthropic rate-limit counter is **per-account, across all devices**. If a teammate burned Account A on their laptop this morning, your Mac sees "Account A: 0% used" and would happily switch you onto an exhausted account.

`aimonitor` defeats this by reading the `anthropic-ratelimit-tokens-remaining` HTTP response header before any auto-switch. The local estimate is a fast first-pass filter; the server-side probe is the gate.

## Features (v1.0.0-beta)

- macOS Sonoma 14+ menu bar widget (native Swift/SwiftUI) showing live session-bar.
- CLI for macOS Sonoma 14+ and Ubuntu 22.04+.
- Multi-account Claude OAuth credential management via macOS Keychain / libsecret.
- Manual `aimonitor switch <label>` between accounts.
- Opt-in auto-switch (default off) with configurable thresholds, gated by a server-side rate-limit probe.
- Single-binary Go daemon, communicating with the widget over a Unix socket.

## Roadmap

- **v1.1**: daily usage chart, cost estimation per account, weekly cap view (once an admin-API data source lands), notarized macOS app.
- **v2.0**: Ubuntu GTK menu bar widget, OpenAI Codex / GitHub Copilot CLI provider.

## Installation

### macOS (Sonoma 14+)

```sh
brew install japananh/tap/aimonitor
```

**First launch**: the `.app` is unsigned in v1.0.0-beta. macOS Gatekeeper will refuse to open it on the first try. Workaround:

```sh
xattr -dr com.apple.quarantine /Applications/AIMonitor.app
open /Applications/AIMonitor.app
```

Or right-click the app in Finder → Open → confirm the Gatekeeper prompt. You only need to do this once. See [`docs/unsigned-app.md`](docs/unsigned-app.md) for the full explanation.

### Ubuntu 22.04+

```sh
curl -fsSL https://aimonitor.dev/install.sh | sh
```

The script installs `aimonitor` to `/usr/local/bin`, registers a `systemd --user` unit, and verifies that `libsecret` is present. The menu bar widget on Linux is coming in v2.0.

## Quick start

```sh
# First run adopts your existing Claude Code credentials.
aimonitor

# Add a second account (opens Claude Code's OAuth flow, then stashes the result).
aimonitor add

# See what aimonitor knows about.
aimonitor list

# Switch the active credential for the next `claude` invocation.
aimonitor switch work

# Check the true server-side remaining quota for an account.
aimonitor probe personal

# Enable auto-switch (default off).
aimonitor config set autoswitch true
aimonitor config set thresholds 40,60,100

# Audit log of every switch.
aimonitor log

# Quick health check.
aimonitor doctor
```

## Privacy

- **No telemetry. No phone-home.** Anywhere.
- OAuth tokens live only in the OS keyring (Keychain on macOS, libsecret on Linux). SQLite holds references, never secrets.
- Token bytes are never logged, even at `--debug` level. Log scrubbing matches `sk-ant-(oat|ort)…`.
- Probe requests (~10 tokens each) are the only network traffic aimonitor initiates; response bodies are discarded after the rate-limit headers are read.

## Building from source

Requires Go 1.24+. On macOS, also requires Xcode 15+ for the menu bar widget.

```sh
git clone https://github.com/japananh/aimonitor
cd aimonitor
make build      # builds the Go binary
make test       # runs unit tests
make widget     # builds AIMonitor.app via xcodebuild (macOS only)
```

## License

MIT. See [`LICENSE`](LICENSE).

## See also

- [`USER_STORIES.md`](USER_STORIES.md) — what v1 ships
- [`docs/architecture.md`](docs/architecture.md) — daemon, IPC, storage
- [`docs/thresholds.md`](docs/thresholds.md) — exactly how the tripwire-and-probe logic decides
- [`docs/security.md`](docs/security.md) — keyring boundaries, scrubbing rules, threat model
