# Architecture

One Go binary plus a thin macOS widget, glued together by SQLite and the
OS keyring. No sockets, no IPC daemons, no telemetry.

```
┌────────────────────┐     reads SQLite      ┌──────────────────────────┐
│  AIMonitor.app     │◀──────────────────────│  aimonitor daemon run    │
│  (Swift menu bar)  │   shells out to CLI   │  usage poller · auto-    │
│                    │──────────────────────▶│  switch · status publish │
└────────────────────┘                       └──────────────────────────┘
          ▲                                              │
          │                    ┌─────────────────────────┼──────────────┐
          │                    ▼                         ▼              │
   Claude Code ──spawns──▶ aimonitor mcp serve     OS keyring      SQLite
   (MCP stdio)             (Slack/ClickUp tools)   (tokens)        (data)
```

## Components

**`aimonitor` (Go, one binary)** hosts three roles:

- **CLI** — `add / import / switch / list / usage / config / doctor / …`.
  Every user-facing action is a CLI subcommand; the widget shells out to
  these rather than reimplementing logic.
- **Daemon** (`aimonitor daemon run`, launchd-managed) —
  - *Usage scheduler*: polls the active account's `/api/oauth/usage`
    every ~5 min ± 30 s jitter (introspection only, consumes no tokens),
    with exponential backoff on errors; inactive accounts are polled on a
    slow round-robin only while their token is valid (never refreshed in
    the background).
  - *Auto-switcher*: dual-threshold (5h / 7d), headroom-based candidate
    selection; see [`thresholds.md`](thresholds.md).
  - *Status publisher*: writes a JSON status row into SQLite every ~2 s —
    this is the widget's data feed.
  - *External-switch watcher*: detects active-account changes made by
    other tools (keyed on account identity, so renames don't false-fire).
  - *Session-log watcher*: tails `~/.claude/projects` JSONL transcripts
    for local session stats; deliberately non-fatal — it can never take
    the daemon down.
- **MCP server** (`aimonitor mcp serve`) — stdio JSON-RPC, spawned by
  Claude Code per session. Serves the Slack + ClickUp tools; resolves
  integration tokens from the keyring per call.

**`AIMonitor.app` (Swift/SwiftUI menu bar)** — displays the daemon-published
status (SQLite reads) and triggers actions by shelling out to the CLI.
It owns no business logic and holds no secrets.

## Storage split

| What | Where | Why |
|---|---|---|
| OAuth credential blobs, integration tokens | OS keyring (macOS Keychain via `/usr/bin/security`; Linux libsecret) | secrets never touch disk files |
| Accounts, usage snapshots, settings, switch audit log | SQLite at `~/Library/Application Support/aimonitor/aimonitor.db` | queryable app data, no secrets |
| Daemon ↔ widget handshake | a JSON status row in the SQLite `settings` table | no sockets to leak or break |

## Switching (two-slot keychain model)

Claude Code reads its credential from one keychain slot
(`Claude Code-credentials`). aimonitor keeps a per-account **stash**
(`aimonitor-<uuid>`) alongside it. A switch:

1. takes a file lock (`~/.aimonitor-lock`) so concurrent switches serialize;
2. snapshots the outgoing live blob back into its stash (captures any
   refresh-token rotation Claude Code performed);
3. refreshes the target's access token if expired (silent, via Anthropic's
   token endpoint), writes it to the live slot;
4. patches `~/.claude.json`'s `oauthAccount` so Claude Code's identity
   matches the new tokens.

Every stash write is **identity-gated**: the live login in `~/.claude.json`
must match the account being written, so attribution races (e.g. a
`claude /login` mid-switch) can't copy one account's credential into
another account's stash. Running `claude` sessions are never touched —
they re-read the keychain and adopt the new account on their own.

## Active-account resolution

Byte-match the live blob against each stash first (exact match is
authoritative), fall back to `~/.claude.json` identity (email + org) when
rotation broke the byte match. Both the scheduler and the status publisher
share this resolver, so "active" is always consistent.
