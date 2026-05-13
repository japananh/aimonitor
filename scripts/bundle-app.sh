#!/usr/bin/env bash
# bundle-app.sh — wrap the Swift-built AIMonitor binary into a .app
# directory layout so the goreleaser archive can ship it alongside the
# CLI. macOS-only; no-ops on every other platform so cross-arch
# releases don't break.
#
# Inputs:  ui/macos/.build/release/AIMonitor (from `swift build -c release`)
#          ui/macos/Resources/Info.plist
# Outputs: dist/AIMonitor.app/Contents/MacOS/AIMonitor
#          dist/AIMonitor.app/Contents/Info.plist
#          dist/AIMonitor.app/Contents/Resources/  (placeholder for icons)
#
# We deliberately don't sign the binary here; signing happens (or
# doesn't, in v1) inside the release workflow where the secrets live.

set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "bundle-app.sh: not on macOS, skipping (.app is mac-only)"
    exit 0
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SWIFT_DIR="$REPO_ROOT/ui/macos"
BIN_IN="$SWIFT_DIR/.build/release/AIMonitor"
PLIST_IN="$SWIFT_DIR/Resources/Info.plist"

# Build the Swift binary if it isn't there yet. Idempotent — `swift build`
# is incremental so a noop call only costs the SPM planning overhead.
echo "==> swift build -c release"
( cd "$SWIFT_DIR" && swift build -c release )

if [[ ! -x "$BIN_IN" ]]; then
    echo "bundle-app.sh: $BIN_IN not built; aborting" >&2
    exit 1
fi
if [[ ! -f "$PLIST_IN" ]]; then
    echo "bundle-app.sh: $PLIST_IN missing; aborting" >&2
    exit 1
fi

APP_OUT="$REPO_ROOT/build/AIMonitor.app"
rm -rf "$APP_OUT"
mkdir -p "$APP_OUT/Contents/MacOS" "$APP_OUT/Contents/Resources"

cp "$BIN_IN" "$APP_OUT/Contents/MacOS/AIMonitor"
chmod +x "$APP_OUT/Contents/MacOS/AIMonitor"
cp "$PLIST_IN" "$APP_OUT/Contents/Info.plist"

# Touch the PkgInfo marker — older versions of Finder pre-Sonoma rely
# on it to detect the bundle type. macOS 14+ doesn't strictly need it
# but it costs us 8 bytes.
printf 'APPL????' > "$APP_OUT/Contents/PkgInfo"

echo "==> wrote $APP_OUT"
echo "==> $(du -sh "$APP_OUT" | cut -f1)"
