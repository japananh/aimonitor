# Security

## Threat model

`aimonitor` is a local-first tool. The threats it tries to defend against:

- A reader of the SQLite database learning OAuth tokens (mitigation: no secrets in SQLite — only `keyring_ref` pointers).
- An attacker with read access to log files extracting tokens (mitigation: log scrubbing of `sk-ant-(oat|ort)…` patterns at every log level).
- A bystander seeing tokens echoed in the terminal (mitigation: aimonitor never prints raw token bytes, even with `--debug`).
- A malicious other process on the same Unix user reading the daemon socket (mitigation: socket bound at `0600` in a `0700` directory).

It does **not** try to defend against:

- A privileged attacker with `root` or your Keychain unlock password.
- A compromised Anthropic API endpoint.
- A keylogger on your machine.

## Where secrets live

| Material | Location |
|---|---|
| Claude OAuth blob (per account) | macOS Keychain (`aimonitor-acct-<uuid>`) or Linux libsecret |
| Process-memory token bytes | Zeroed via `Credential.Zero()` immediately after use |
| Active credential for the underlying CLI | macOS Keychain (`Claude Code-credentials`) — the slot Claude Code itself reads |

## Where pointers and metadata live

| Data | Location |
|---|---|
| SQLite database | `~/Library/Application Support/aimonitor/aimonitor.db` (macOS) at `0600` |
| Daemon socket | `~/Library/Application Support/aimonitor/daemon.sock` at `0600` |
| Config YAML | `~/.config/aimonitor/config.yaml` at `0600` |
| Logs | stderr by default; optional file at `~/Library/Logs/aimonitor/aimonitor.log` at `0600` |

## Telemetry

None. `aimonitor` initiates exactly two kinds of outbound traffic, both to Anthropic and both with the user's own credentials:

1. The OAuth flow during `aimonitor add` (delegated to `claude login`).
2. The OAuth token refresh (`platform.claude.com/v1/oauth/token`) performed silently on switch or when a stored token nears expiry.

No analytics, error reporting, or update checks call any other host.
