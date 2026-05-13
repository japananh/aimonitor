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
- Single-binary Go daemon; widget reads state from a shared SQLite snapshot and mutates via the `aimonitor` CLI (no separate IPC server to manage).

## Roadmap

Directional, not committed. See [`ROADMAP.md`](ROADMAP.md) for gating conditions and rationale.

- **v1.1**: daily usage chart, cost estimation per account, notarized macOS app.
- **v1.2.x**: weekly cap view (gated on a server-side data source available to non-admin users).
- **v2.0**: Ubuntu GTK menu bar widget, second `Provider` implementation (Codex or Copilot CLI).

## Installation

### macOS (Sonoma 14+)

`aimonitor` lives in a third-party Homebrew tap (`japananh/homebrew-tap`) because it's a new project that hasn't been submitted to `homebrew/core`. Two equivalent install paths:

**One-liner** (taps + installs in a single command):

```sh
brew install --cask japananh/tap/aimonitor
```

**Or tap once, install short-form forever** (preferred if you expect to install other things from this tap later):

```sh
brew tap japananh/tap
brew install --cask aimonitor
```

Either way you get the CLI binary `aimonitor` on your `$PATH` and `AIMonitor.app` in `/Applications`.

**First launch — clear the unsigned-binary quarantine**: the `.app` is unsigned in v1.0.0-beta (notarization is a v1.1 deliverable). macOS Gatekeeper will refuse to open it on the first try. Workaround:

```sh
xattr -dr com.apple.quarantine /Applications/AIMonitor.app
open /Applications/AIMonitor.app
```

Or right-click the app in Finder → Open → confirm the Gatekeeper prompt. You only need to do this once. See [`docs/unsigned-app.md`](docs/unsigned-app.md) for the full explanation.

**Upgrading later**:

```sh
brew update              # picks up new versions from every tapped repo
brew upgrade --cask aimonitor
```

### Ubuntu 22.04+

```sh
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh
```

The script installs `aimonitor` to `/usr/local/bin`, registers a `systemd --user` unit, and verifies that `libsecret` is present. The menu bar widget on Linux is deferred to v2.0; the CLI is fully functional.

Re-running the script upgrades in place (idempotent). To pin a specific version, set `AIMONITOR_VERSION=v1.0.0-beta.1` before piping.

### Uninstall

```sh
aimonitor uninstall              # disables autostart; preserves your data
aimonitor uninstall --purge      # also drops the SQLite DB, config, and aimonitor keyring entries
```

Then on macOS:

```sh
brew uninstall --cask aimonitor
brew untap japananh/tap          # optional: forget the tap entirely
```

Your original `Claude Code-credentials` keyring entry is **never touched** by aimonitor's uninstall — existing `claude` CLI logins keep working.

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

Requires Go 1.25+ (transitively required by `modernc.org/sqlite`). On macOS, also requires the Swift toolchain (ships with the Xcode Command Line Tools — `xcode-select --install`) for the menu bar widget. Full Xcode is **not** required; the widget is built via Swift Package Manager headlessly.

```sh
git clone https://github.com/japananh/aimonitor
cd aimonitor
make build              # builds the Go CLI binary
make test               # runs unit tests
make widget             # builds AIMonitor.app via SPM (macOS only)
make release-snapshot   # full goreleaser dry-run (no publish; needs goreleaser installed)
```

## License

MIT. See [`LICENSE`](LICENSE).

## See also

- [`USER_STORIES.md`](USER_STORIES.md) — what v1 ships
- [`docs/architecture.md`](docs/architecture.md) — daemon, IPC, storage
- [`docs/thresholds.md`](docs/thresholds.md) — exactly how the tripwire-and-probe logic decides
- [`docs/security.md`](docs/security.md) — keyring boundaries, scrubbing rules, threat model
