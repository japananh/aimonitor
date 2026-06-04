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
# We re-sign the bundle with `codesign --force --sign -` at the end.
# Swift's build process already ad-hoc-signs the binary, but with the
# `linker-signed` flag — and macOS Sonoma+ won't persist Keychain ACL
# grants for linker-signed binaries. A manual ad-hoc sign clears that
# flag without requiring a Developer ID certificate (v1.1's deliverable).

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
# App icon (Dock + Finder). Pre-built .icns committed in Resources/;
# regenerate from assets/icon.svg via qlmanage + sips + iconutil.
cp "$SWIFT_DIR/Resources/AppIcon.icns" "$APP_OUT/Contents/Resources/AppIcon.icns"

# Touch the PkgInfo marker — older versions of Finder pre-Sonoma rely
# on it to detect the bundle type. macOS 14+ doesn't strictly need it
# but it costs us 8 bytes.
printf 'APPL????' > "$APP_OUT/Contents/PkgInfo"

# Re-sign the bundle with a manual ad-hoc identity. Codesigning a
# directory (the .app) walks Contents/MacOS and signs each Mach-O
# inside it, plus the bundle structure itself. Without this, Swift's
# linker-signed flag prevents Keychain ACL persistence on Sonoma+.
echo "==> codesign --force --sign - (ad-hoc)"
codesign --force --sign - --identifier dev.aimonitor.AIMonitor --deep "$APP_OUT"

echo "==> wrote $APP_OUT"
echo "==> $(du -sh "$APP_OUT" | cut -f1)"
echo "==> signature info:"
codesign -dv --verbose=2 "$APP_OUT" 2>&1 | grep -E '^(Identifier|Format|CodeDirectory|flags|Signature)' | sed 's/^/    /'
