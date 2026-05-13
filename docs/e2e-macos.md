# End-to-end verification: macOS Sonoma 14+

This is the verification checklist for the `v1.0.0-beta.1` Mac release.
Run it on a clean install (or after `aimonitor uninstall --purge`).

Tick each box after the listed behavior is confirmed.

## 0. Prerequisites

- [ ] Mac running macOS 14 (Sonoma) or newer
- [ ] An existing Claude Code login (so we have a credential to import)
- [ ] Homebrew installed (`brew --version` works)

## 1. Install

```bash
brew tap japananh/tap
brew install --cask aimonitor
```

- [ ] Both `aimonitor` (CLI) and `AIMonitor.app` end up installed.
- [ ] `aimonitor --version` prints `v1.0.0-beta.1`.

## 2. First-open Gatekeeper workaround

```bash
xattr -dr com.apple.quarantine /Applications/AIMonitor.app
open /Applications/AIMonitor.app
```

- [ ] The chart-bar icon appears in the menu bar (no Dock icon).
- [ ] Clicking the icon shows the popover with a "Daemon not running" hint.

## 3. Daemon import / first account

```bash
aimonitor add
```

- [ ] Prompts for a label; defaults to the existing account's email.
- [ ] After confirming, `aimonitor list` shows the account.
- [ ] `Claude Code-credentials` Keychain entry is **unchanged**
      (verify in Keychain Access — same modification date).

## 4. Add a second account

```bash
aimonitor add
```

- [ ] `claude login` opens a browser tab; complete the flow with a
      different account.
- [ ] After it completes, `aimonitor list` shows two rows.
- [ ] Cancel mid-flow (try a third `aimonitor add` then Ctrl-C the
      browser tab) — the stash is restored, no half-state.

## 5. Switch

```bash
aimonitor switch <label>
```

- [ ] Output: `next claude launch will use <label>`.
- [ ] Open a terminal, run `claude` — the new account is active.

## 6. Status reflects local usage

Run `claude` for a minute (any prompts work).

- [ ] `aimonitor status` reflects updated usage within 5 s.
- [ ] Menu bar widget's session bar updates within 5 s.

## 7. Server-side probe

```bash
aimonitor probe <label> --refresh
```

- [ ] Output: `<label> <tokens_remaining> <reset_at> probed`.
- [ ] Re-run without `--refresh` — shows `cached`.
- [ ] Probe on an obviously-bad token (rename a stash to point at a
      garbage key) returns `HTTP 401` cleanly, no panic.

## 8. Auto-switch end-to-end

```bash
aimonitor config set autoswitch true
# Burn quota on one account until past 40% local
```

- [ ] After ~40% local, the daemon probes candidates and switches.
- [ ] `aimonitor log` shows a `trigger=autoswitch` row with both
      `from_probed_remaining` and `to_probed_remaining` populated.
- [ ] The 60 s cool-down prevents back-to-back switches.

## 9. Auto-switch disabled does NOT switch

```bash
aimonitor config set autoswitch false
```

- [ ] Burning quota past 40% does NOT switch.
- [ ] `aimonitor log` shows no new auto-switch rows.

## 10. Widget interaction

- [ ] Click an account row's `Switch` button — within ~1 s, the active
      account changes and the row badge moves.
- [ ] Open Preferences, toggle off the per-account panel — the popover
      now shows only the session bar.
- [ ] Toggle on `Launch AIMonitor at login`, log out + log back in —
      the menu bar icon is present without manual launch.

## 11. Daemon autostart

```bash
aimonitor config set autostart true
launchctl print "gui/$UID/dev.aimonitor.daemon" | head -10
```

- [ ] launchctl confirms the LaunchAgent is loaded.
- [ ] Reboot the Mac, log back in — `pgrep -f "aimonitor daemon"` shows
      a running process.

## 12. Uninstall

```bash
aimonitor uninstall          # no --purge
brew uninstall --cask aimonitor
```

- [ ] LaunchAgent gone (`launchctl print` returns non-zero).
- [ ] SQLite DB still present (since no `--purge`).
- [ ] Re-install — old data is intact (label list survived).

Then:

```bash
brew install --cask aimonitor
aimonitor uninstall --purge
```

- [ ] DB, config YAML, and aimonitor-namespaced Keychain entries all
      removed.
- [ ] `Claude Code-credentials` Keychain entry **untouched** (Claude
      Code still works after uninstall — verify with `claude`).

---

If every box is ticked, mark `v1.0.0-beta.1` as verified on macOS.
