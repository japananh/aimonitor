# Roadmap

> **Status: directional, not committed.** This document describes the maintainer's current intent. Items may move between versions, be deferred, or be cut entirely. Open an issue to discuss anything you're depending on.

The roadmap is organized by version, with **gating conditions** instead of calendar dates. A version ships when its conditions are met, not when a month rolls over.

| Version | Milestone | Theme |
|---|---|---|
| [v1.0.0-beta](https://github.com/japananh/aimonitor/milestone/1) | active | Monitor + manual switch + opt-in auto-switch (probe-gated) on macOS |
| [v1.1](https://github.com/japananh/aimonitor/milestone/2) | next | Analytics + notarized .app |
| [v2.0](https://github.com/japananh/aimonitor/milestone/3) | future | Ubuntu widget + multi-provider |

---

## v1.0.0-beta — Monitor + manual switch + opt-in auto-switch (macOS only)

**The hypothesis we're testing**: a local-first menu bar tool that watches Claude Code's JSONL transcripts, lets you stash multiple OAuth credentials, and gates auto-switch on a server-side rate-limit-header probe is enough to solve the shared-Team-account "bad-guys burn quota" problem.

**In scope**

- CLI for macOS Sonoma 14+ and Ubuntu 22.04+ (`add`, `list`, `switch`, `status`, `config`, `probe`, `log`, `daemon`, `doctor`, `uninstall`).
- macOS menu bar widget (Swift/SwiftUI): required session-bar panel + toggleable per-account headroom table.
- Multi-account Claude OAuth credential management via macOS Keychain / Linux libsecret.
- Auto-switch (default off) with configurable ascending-int tripwires and a server-side rate-limit-header probe gate.
- Single Go daemon binary; Unix socket JSON-RPC to the widget.
- Auto-start on login (SMAppService on macOS, `systemd --user` on Linux).
- Install pipeline: Homebrew tap for macOS (unsigned .app — `docs/unsigned-app.md` documents the first-open workaround), `curl | sh` for Linux.

**Out of scope (intentionally deferred)**

- Daily / 30-day usage chart → v1.1
- Cost estimation → v1.1
- Weekly cap view → v1.x once a server-side data source is available
- Notarized .app → v1.1
- Ubuntu GTK widget → v2.0
- Codex / GitHub Copilot CLI provider → v2.0

**Gating conditions for ship**

- All Phase 1–6 issues in the v1.0.0-beta milestone closed.
- `aimonitor doctor` green on a fresh macOS Sonoma install AND a fresh Ubuntu 22.04 install.
- End-to-end verification scenarios from `_plans/kind-painting-noodle.md` §11 pass.

---

## v1.1 — Analytics + notarized .app

**Why this is next**: once auto-switch works for the burn-account problem, users will want to *understand* their usage (which account, when, how much it cost). Analytics is what turns the tool from "panic button" into "everyday companion."

**In scope**

- Daily / 30-day usage chart panel in the menu bar widget (Swift Charts — column or line, decided by UX testing).
- Cost estimation per account, using a maintained `prices.json` shipped with the binary plus a user override file.
- Cost column in `aimonitor list`.
- Apple-notarized `AIMonitor.app`. README first-open workaround disappears.
- Light theming polish on the widget popover.

**Gating conditions**

- v1.0.0-beta has had at least 30 days of field use without any credential-corruption incident.
- A maintenance plan for `prices.json` is committed (likely: a CI job that scrapes Anthropic's published pricing and opens a PR).
- Apple Developer Program enrolled and signing identity configured in GitHub Actions secrets.

---

## v1.2.x — Weekly cap view (depends on data-source availability)

**Why this is its own slot**: weekly cap can only be shown accurately with server-side data. The local JSONL can't see other devices on the account. Until an admin API or a similar source exists for non-admin users, this stays out.

**In scope**

- Weekly cap panel in the widget, labeled with its data source.
- Either: optional admin API key support, or: a much more aggressive probe schedule that approximates weekly consumption.

**Gating conditions**

- A data source exists that is accurate AND available to non-admin users on Team plans.

---

## v2.0 — Ubuntu menu bar widget + multi-provider

**Why bundled**: Ubuntu users were promised a complete product, not a CLI-only half-step. Codex/Copilot users were promised they'd be first-class, not bolted-on. Both ride the same `Provider` interface that's already in v1.0.0-beta.

**In scope**

- Ubuntu GTK4 menu bar widget (or AppIndicator fallback for desktops without StatusNotifier support). Talks to the same Go daemon over the same Unix socket.
- Second `Provider` implementation: OpenAI Codex CLI **or** GitHub Copilot CLI (decided closer to release).
- Multi-provider per-account configuration in the CLI and widget.

**Gating conditions**

- The chosen second provider has a stable, documented authentication mechanism that supports per-machine OAuth blobs (no provider-locked credential vault).
- A Linux contributor (or this maintainer) has a daily-driver Ubuntu setup to test against.
- The `Provider` interface has survived v1.1 without breaking changes.

---

## Not on the roadmap

If something here doesn't surprise you, that's intentional — these are the things people might *expect* but that aren't planned:

- **Windows support.** Outside the maintainer's daily environment. PRs welcome but unlikely to be maintainer-driven.
- **Mobile companion app.** The use case is local-first developer tooling, which doesn't translate.
- **Cloud-hosted dashboard.** Aimonitor is local-first and has zero-telemetry as a positioning commitment. A hosted version would invert the trust model.
- **Provider-side workarounds for the burn-account problem.** That's Anthropic's product, not ours.

---

## Contributing to the roadmap

- If you'd like to argue for or against an item: open an issue tagged `roadmap-discussion`.
- If you want to *build* an item: open an issue tagged `claim` so we don't duplicate work; pair on design before code.
- Items can be advanced (or cut) between versions; this file is updated when that happens.
