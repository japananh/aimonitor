# aimonitor — v1.0.0-beta User Stories

> Source of truth for what v1 ships. Reviewed alongside `_plans/kind-painting-noodle.md` (implementation plan).
> Edit this file directly to refine stories — the plan derives from these, not the other way around.

## Personas

- **Primary**: developer who shares Anthropic Claude Team seats with teammates whose email aliases all funnel to one inbox. Non-admin. Cannot revoke other devices via claude.ai. Real risk: teammates burn quota on the shared accounts.
- **Secondary**: solo developer juggling personal + work Claude accounts on the same machine.

## Definitions

- **Local % used**: tokens summed from `~/.claude/projects/**/*.jsonl` on **this** machine, divided by the observed-maximum-per-session budget. Cheap, instant, but blind to other devices on the same account.
- **Server-side remaining**: ground truth from Anthropic's `anthropic-ratelimit-tokens-remaining` response header, obtained by issuing one tiny probe request.
- **Tripwire**: a configured threshold in `(0, 100]`. When the active account's local % crosses one, the auto-switch engine evaluates candidates.
- **Candidate**: an account whose local % used is strictly below the just-crossed tripwire (excluding the currently-active account).

---

## Epic A — Install & First Run

- **A1.** As a macOS user, I install aimonitor with `brew install japananh/tap/aimonitor` and end up with both the `aimonitor` CLI and `AIMonitor.app` (unsigned; menu bar icon visible after first-open right-click → Open). **Acceptance:** one command, README documents the first-open prompt, `aimonitor --version` returns the build version.
- **A2.** As an Ubuntu user, I install aimonitor with `curl -fsSL https://aimonitor.dev/install.sh | sh` and end up with the `aimonitor` CLI in `/usr/local/bin` + a `systemd --user` unit. **Acceptance:** install script is idempotent, prints next steps, exits non-zero on libsecret missing.
- **A3.** As a user with an existing Claude Code login, I run `aimonitor` for the first time and it offers to adopt my current `Claude Code-credentials` Keychain entry under a label I choose (default: my Anthropic account email). **Acceptance:** confirmation prompt, no silent take-over, original Keychain entry left untouched.
- **A4.** As a user, I uninstall with `aimonitor uninstall` (which `brew uninstall` also calls). With `--purge` it drops the SQLite DB, removes aimonitor-namespaced Keychain entries, and unregisters the LaunchAgent. Without `--purge`, data is preserved. **Acceptance:** `--purge` removes all artifacts; idempotent.

## Epic B — Account Management

- **B1.** As a user, I run `aimonitor add` and aimonitor:
  1. Reads & stashes the current `Claude Code-credentials` blob in memory.
  2. Invokes `claude login` and waits for exit code 0 (which means OAuth completed and the blob has been written to `Claude Code-credentials`).
  3. Reads the now-overwritten blob — that's the new account.
  4. Moves the new blob into an aimonitor-namespaced Keychain entry under a label I pick.
  5. Restores the original blob into `Claude Code-credentials` so my previously-active account stays active.

  **Acceptance:** No window where `Claude Code-credentials` is left empty. If `claude login` is cancelled or fails, the stash is restored. Memory containing token bytes is zeroed before return.
- **B2.** As a user, I run `aimonitor list` and see a table: label · email · % session used (local estimate) · last used · token status (fresh/stale). **Acceptance:** offline-safe (no live API call required); stale tokens flagged visually.
- **B3.** As a user, I run `aimonitor switch <label>` to swap the active credential into the `Claude Code-credentials` slot. **Acceptance:** writes within 200 ms; prints "next `claude` launch will use <label>"; warns if a `claude` process is currently running (the running process won't pick up the new token).
- **B4.** As a user, I run `aimonitor remove <label>` to delete an account from aimonitor. **Acceptance:** confirmation prompt unless `--yes`; Keychain entry and SQLite row both removed; refuses to remove the currently-active account without `--force`.
- **B5.** As a user, I run `aimonitor rename <old> <new>` to relabel an account. **Acceptance:** label uniqueness enforced.

## Epic C — Usage Monitoring

- **C1.** As a user, I run `aimonitor status` and see the current account's session-window usage (locally estimated tokens used, tripwire bands crossed, time-to-reset). **Acceptance:** updates within 5 s of a new JSONL line being appended.
- **C2.** As a user, I open the menu bar widget and see a session % bar that updates live. **This panel cannot be disabled.** **Acceptance:** bar reflects same data as `aimonitor status`, refreshes when daemon emits an update.
- **C3.** As a user, I expand the widget and see a per-account headroom table (label · % used local · status · last-switched). I can right-click a row and pick "Switch to this account". **Acceptance:** clicking refreshes within 1 s.
- **C4.** As a user, I toggle off the per-account panel from widget Preferences. **Acceptance:** preference is persisted; session bar remains visible.

## Epic D — Auto-switch (opt-in)

- **D1.** As a user, I run `aimonitor config set autoswitch true` to enable auto-switch. **Default is false.** **Acceptance:** clear CLI confirmation; widget shows an "auto-switch ON" indicator.
- **D2.** As a user, I run `aimonitor config set thresholds 40,60,100` to update the tripwire list. **Validation:** at least one int, all in (0, 100], strictly ascending. Invalid input rejected with a specific error. **Acceptance:** `aimonitor config get thresholds` round-trips.
- **D3.** As a user with auto-switch on, when my active account's local session % crosses a tripwire T, the daemon:
  1. Filters configured accounts to those with local % used `< T` (excluding the current one). **Threshold rule: candidate must be below the just-crossed tripwire.**
  2. For each surviving candidate (capped at top-K by local-% used, K=3), runs a **server-side rate-limit probe** to fetch its true `anthropic-ratelimit-tokens-remaining`.
  3. Picks the candidate with the highest server-side remaining tokens, but only if strictly higher than the current account's probed remaining.
  4. Swaps the Keychain blob, emits a desktop notification, writes a `switch_audit` row.

  **Acceptance:** never switches if no probed candidate is better than current; cool-down ≥ 60 s between switches to prevent thrashing.
- **D4.** As a user, I run `aimonitor log` and see the last N switch events (timestamp · from → to · trigger · local %s · probed-remaining). **Acceptance:** stored in SQLite `switch_audit` table; default N=20, configurable.
- **D5.** As a user, I run `aimonitor probe <label>` to manually run a server-side rate-limit probe against an account and see its true remaining quota. **Acceptance:** prints `remaining_tokens`, `reset_at`, and the local-estimate side-by-side; never alters Keychain.

## Epic E — Daemon & Auto-start

- **E1.** As a user, after install the daemon launches on login. On macOS this is `SMAppService.daemon` registered by a helper inside `AIMonitor.app`; on Linux it is a `systemd --user` unit. **Acceptance:** survives logout/login; `aimonitor doctor` reports daemon status.
- **E2.** As a user, I run `aimonitor daemon stop|start|restart|status` to control the daemon manually. **Acceptance:** commands are idempotent; `status` reports PID + uptime.
- **E3.** As a user, when the daemon restarts mid-day it resumes JSONL scanning from the last byte offset it processed per file. **Acceptance:** no double-counting; offsets persisted in SQLite.

## Epic F — Widget Preferences

- **F1.** As a user, I open widget Preferences and enable/disable each non-required panel. Session bar is always on. **Acceptance:** prefs persist in SQLite; widget reflects on the next render tick.
- **F2.** As a user, I toggle auto-start on/off from Preferences without using the CLI. **Acceptance:** matches `aimonitor config set autostart`.

## Epic G — Diagnostics

- **G1.** As a user with a problem, I run `aimonitor doctor` which prints: daemon status, JSONL parser health (last N files seen, errors), socket reachability, Keychain access OK, SQLite DB writable, last successful probe per account. **Acceptance:** completes in < 3 s; exits non-zero on any failed check.
