#!/usr/bin/env bash
# install.sh — pull the latest aimonitor release from GitHub, drop the
# binary into /usr/local/bin, sanity-check libsecret, and (optionally)
# wire up the systemd --user unit.
#
# Designed to be piped:
#
#     curl -fsSL https://raw.githubusercontent.com/japananh/aimonitor/main/packaging/linux/install.sh | sh
#
# The script is idempotent. Re-running upgrades the binary in place.

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

# --- platform detection ---------------------------------------------------
OS="$(uname -s)"
ARCH="$(uname -m)"
if [[ "$OS" != "Linux" ]]; then
    err "aimonitor's curl-installer is Linux-only. macOS users should:"
    err "  brew install japananh/tap/aimonitor"
    exit 2
fi
case "$ARCH" in
    x86_64|amd64)  ARCH_TAG=amd64 ;;
    aarch64|arm64) ARCH_TAG=arm64 ;;
    *)             err "Unsupported arch: $ARCH"; exit 2 ;;
esac

# --- dependency sanity check ---------------------------------------------
log "==> Checking dependencies"
need_libsecret=true
if command -v secret-tool >/dev/null 2>&1; then
    need_libsecret=false
elif ldconfig -p 2>/dev/null | grep -q libsecret-1.so; then
    need_libsecret=false
fi
if $need_libsecret; then
    err "libsecret is missing — aimonitor stores OAuth tokens in the Secret Service."
    err "Install via your package manager, e.g.:"
    err "  apt-get install libsecret-tools libsecret-1-0  # Debian/Ubuntu"
    err "  dnf install libsecret                          # Fedora"
    err "  pacman -S libsecret                            # Arch"
    exit 3
fi
ok "  libsecret ✓"

# --- resolve latest release tag ------------------------------------------
log "==> Resolving latest release"
REPO="japananh/aimonitor"
LATEST_TAG="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -oE '"tag_name":\s*"[^"]+"' | head -1 | cut -d'"' -f4)"
if [[ -z "${LATEST_TAG:-}" ]]; then
    err "Could not determine latest release tag from GitHub API."
    err "Set AIMONITOR_VERSION=vX.Y.Z explicitly and retry."
    exit 4
fi
VERSION="${AIMONITOR_VERSION:-$LATEST_TAG}"
VERSION_NUM="${VERSION#v}"
ok "  installing aimonitor $VERSION ($ARCH_TAG)"

# --- download + verify checksum ------------------------------------------
TARBALL="aimonitor_${VERSION_NUM}_linux_${ARCH_TAG}.tar.gz"
URL="https://github.com/$REPO/releases/download/$VERSION/$TARBALL"
SUM_URL="https://github.com/$REPO/releases/download/$VERSION/checksums.txt"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
log "==> Downloading $TARBALL"
curl -fsSL "$URL"      -o "$TMP/$TARBALL"
curl -fsSL "$SUM_URL"  -o "$TMP/checksums.txt"

log "==> Verifying checksum"
( cd "$TMP" && grep "  $TARBALL\$" checksums.txt | sha256sum -c - )
ok "  checksum ✓"

log "==> Extracting"
tar -xzf "$TMP/$TARBALL" -C "$TMP"

# --- install binary -------------------------------------------------------
INSTALL_DIR="${AIMONITOR_PREFIX:-/usr/local/bin}"
SUDO=""
if [[ ! -w "$INSTALL_DIR" ]]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
        warn "  $INSTALL_DIR is not writable; using sudo"
    else
        err "$INSTALL_DIR is not writable and sudo is missing. Set AIMONITOR_PREFIX=\$HOME/.local/bin and retry."
        exit 5
    fi
fi
$SUDO install -m 0755 "$TMP/aimonitor" "$INSTALL_DIR/aimonitor"
ok "  installed $INSTALL_DIR/aimonitor"

# --- systemd user unit (opt-in) ------------------------------------------
log "==> Configuring autostart"
if command -v systemctl >/dev/null 2>&1; then
    if "$INSTALL_DIR/aimonitor" config set autostart true >/dev/null; then
        ok "  systemd --user unit enabled (run 'aimonitor config set autostart false' to disable)"
    else
        warn "  failed to enable autostart automatically; run 'aimonitor config set autostart true' manually"
    fi
else
    warn "  systemctl not found; skipping autostart wiring"
fi

# --- next steps -----------------------------------------------------------
ok "aimonitor $VERSION installed."
cat <<EOF

Next steps:
  1. aimonitor add               # import / add a Claude OAuth account
  2. aimonitor list              # see your accounts
  3. aimonitor config get autoswitch   # auto-switch defaults to OFF
  4. aimonitor doctor            # run a health check

Uninstall:
  aimonitor uninstall            # disable autostart + remove binary state
  aimonitor uninstall --purge    # also drop DB + libsecret entries
EOF
