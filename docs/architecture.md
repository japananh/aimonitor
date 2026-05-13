# Architecture

> Stub — full content lands during Phase 2. The implementation plan (`_plans/kind-painting-noodle.md`) is the current source of truth for architectural decisions.

## High level

- One Go binary, `aimonitor`, hosting both the CLI subcommands and the background daemon (`aimonitor daemon run`).
- On macOS, a separate Swift menu bar app (`AIMonitor.app`) talks to the daemon over a Unix socket.
- Storage split: SQLite for app data, OS keyring (macOS Keychain / Linux libsecret) for OAuth blobs.

See `_plans/kind-painting-noodle.md` §4 for the diagram and full responsibilities of each module.
