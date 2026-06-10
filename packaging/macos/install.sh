#!/usr/bin/env bash
# install.sh — one-command macOS installer for aimonitor. Wraps the four
# steps Homebrew otherwise makes you run by hand: tap the cask repo, trust
# it (newer Homebrew refuses third-party taps until you do), install (or
# upgrade) the cask, and clear the Gatekeeper quarantine on the unsigned
# .app so it opens on first launch.
#
# Designed to be piped:
#
#     curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/macos/install.sh | bash
#
# Idempotent. Re-running upgrades an existing install in place.

set -euo pipefail

# --- pretty output --------------------------------------------------------
RESET=$'\033[0m'
BOLD=$'\033[1m'
RED=$'\033[31m'
GREEN=$'\033[32m'
YELLOW=$'\033[33m'

log()  { printf '%s%s%s\n' "$BOLD" "$*" "$RESET"; }
warn() { printf '%s%s%s\n' "$YELLOW" "$*" "$RESET" >&2; }
err()  { printf '%s%s%s\n' "$RED"   "$*" "$RESET" >&2; }
ok()   { printf '%s%s%s\n' "$GREEN" "$*" "$RESET"; }

APP="/Applications/AIMonitor.app"
TAP="japananh/tap"

# --- platform + brew sanity check ----------------------------------------
if [[ "$(uname -s)" != "Darwin" ]]; then
    err "This installer is macOS-only. Linux users:"
    err "  curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh"
    exit 2
fi
if ! command -v brew >/dev/null 2>&1; then
    err "Homebrew is required. Install it from https://brew.sh and re-run."
    exit 3
fi

# --- tap + trust ----------------------------------------------------------
# `brew trust` only exists on Homebrew builds that enforce the third-party
# tap gate; it's a no-op (and may not exist) on older ones, so best-effort.
log "==> Tapping $TAP"
brew tap "$TAP" >/dev/null 2>&1 || true
brew trust "$TAP" >/dev/null 2>&1 || true

# --- install or upgrade ---------------------------------------------------
if brew list --cask aimonitor >/dev/null 2>&1; then
    log "==> Upgrading aimonitor"
    brew upgrade --cask aimonitor || ok "  already on the latest version"
else
    log "==> Installing aimonitor"
    brew install --cask aimonitor
fi

# --- clear Gatekeeper quarantine (unsigned .app) -------------------------
if [[ -d "$APP" ]]; then
    log "==> Clearing Gatekeeper quarantine"
    xattr -dr com.apple.quarantine "$APP" 2>/dev/null || true
    ok "  $APP ready to open"
fi

# --- launch + next steps --------------------------------------------------
open -a AIMonitor 2>/dev/null || true
ok "aimonitor installed."
cat <<EOF

Next steps:
  1. aimonitor add --adopt-current --label personal   # register your current Claude login
  2. aimonitor add --label work                        # add another account
  3. aimonitor list                                    # live 5h / 7d usage per account
  4. aimonitor doctor                                  # health check

The menu bar icon appears once the daemon publishes its first status (a few seconds).
EOF
