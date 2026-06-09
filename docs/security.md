# Security

## Threat model

`aimonitor` is a local-first tool. The threats it tries to defend against:

- A reader of the SQLite database learning OAuth tokens (mitigation: no secrets in SQLite — only `keyring_ref` pointers).
- An attacker with read access to log files extracting tokens (mitigation: token bytes are never written to logs — the daemon logs only account labels, usage percentages, and errors).
- A bystander seeing tokens echoed in the terminal (mitigation: aimonitor never prints raw token bytes, even with `--debug`).
- A malicious process under the same Unix user reading secrets off disk (mitigation: no secrets are written to disk — credentials live only in the OS keyring; SQLite and the config file hold references and settings, never tokens, and are `0600`).

It does **not** try to defend against:

- A privileged attacker with `root` or your Keychain unlock password.
- A compromised Anthropic API endpoint.
- A keylogger on your machine.

## Where secrets live

| Material | Location |
|---|---|
| Claude OAuth blob (per account) | macOS Keychain (`aimonitor-<uuid>`) or Linux libsecret |
| Process-memory token bytes | Zeroed via `Credential.Zero()` immediately after use |
| Active credential for the underlying CLI | macOS Keychain (`Claude Code-credentials`) — the slot Claude Code itself reads |

## Where pointers and metadata live

| Data | Location |
|---|---|
| SQLite database | `~/Library/Application Support/aimonitor/aimonitor.db` (macOS) at `0600` |
| Daemon ↔ widget handshake | a JSON status row in the SQLite `settings` table (no socket) |
| Config YAML | `~/.config/aimonitor/config.yaml` at `0600` |
| Logs | stderr by default; optional file at `~/Library/Logs/aimonitor/aimonitor.log` at `0600` |

## Telemetry

None. `aimonitor` initiates four kinds of outbound traffic, and nothing about you is ever sent:

1. The OAuth flow during `aimonitor add` (delegated to `claude login`) — Anthropic.
2. Usage introspection, `GET /api/oauth/usage` (~5 min for the active account, consumes no tokens) — Anthropic.
3. The OAuth token refresh (`platform.claude.com/v1/oauth/token`), performed silently on switch or when a stored token nears expiry — Anthropic.
4. The release check (`GET api.github.com/repos/japananh/aimonitor/releases`) when `auto_update.enabled` — GitHub. It compares version numbers only and never auto-installs.

No analytics and no error reporting. The only non-Anthropic host contacted is `api.github.com`, and only to compare versions.
