# End-to-end verification: Ubuntu 22.04+

This is the verification checklist for `v1.0.0-beta.1` on Linux. No
menu bar widget on Linux in v1 — that's a v2.0 deliverable. Verify the
CLI surfaces here.

## 0. Prerequisites

- [ ] Ubuntu 22.04 LTS or newer (other distros likely fine but unsupported)
- [ ] libsecret installed (`secret-tool --version` works)
- [ ] gnome-keyring or KeePassXC running (anything that implements the
      org.freedesktop.secrets D-Bus interface)
- [ ] An existing Claude Code login

## 1. Install via the one-liner

```bash
curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh
```

- [ ] Installer prints version, downloads the right arch tarball, and
      verifies the checksum.
- [ ] `aimonitor --version` prints `v1.0.0-beta.1`.
- [ ] `systemctl --user is-enabled aimonitor.service` returns `enabled`.

## 2. First-account import

```bash
aimonitor add
```

- [ ] Prompts for a label; default is the account email.
- [ ] `aimonitor list` shows the new row.
- [ ] `secret-tool search service "Claude Code-credentials"` still
      returns the original entry untouched.

## 3. Second account

```bash
aimonitor add
```

- [ ] `claude login` opens a browser (or prints the URL on a headless box).
- [ ] After completion, `aimonitor list` shows two accounts.
- [ ] Cancel a third `aimonitor add` mid-flow with Ctrl-C — the stash
      is restored to its previous value.

## 4. Switch + status

```bash
aimonitor switch <label>
claude  # run a prompt
aimonitor status
```

- [ ] `aimonitor status` reflects new usage within 5 s.

## 5. Probe

```bash
aimonitor probe --all --refresh
```

- [ ] Two rows, both showing `HTTP 200` and a tokens_remaining number.
- [ ] Rerun without `--refresh` — at least one row says `cached`.

## 6. Auto-switch

```bash
aimonitor config set autoswitch true
# Run claude until > 40% local on one account
```

- [ ] Daemon probes candidates, switches when one has strictly more
      remaining tokens.
- [ ] `aimonitor log --limit 5` shows the audit row.

## 7. Daemon lifecycle

```bash
systemctl --user restart aimonitor
journalctl --user -u aimonitor -n 50
```

- [ ] Restart succeeds; logs show watcher + auto-switcher initialisation.
- [ ] No token bytes in the logs (grep for `sk-ant-`).

## 8. Doctor

```bash
aimonitor doctor
```

- [ ] All checks green: config load, SQLite open, claude provider,
      JSONL dir, keyring round-trip, account count, per-account probe
      freshness.

## 9. Uninstall

```bash
aimonitor uninstall          # no --purge
```

- [ ] systemd user unit disabled + file removed.
- [ ] SQLite DB still present.

```bash
aimonitor uninstall --purge
sudo rm /usr/local/bin/aimonitor
```

- [ ] DB, config YAML, libsecret aimonitor-namespaced entries gone.
- [ ] `secret-tool search service "Claude Code-credentials"` STILL
      returns the original entry — Claude Code continues to work.

---

If every box is ticked, mark `v1.0.0-beta.1` as verified on Ubuntu.
