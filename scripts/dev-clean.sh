#!/usr/bin/env bash
# dev-clean.sh — purge dev AIMonitor bundles and their stale LaunchServices
# entries. `make widget` builds build/AIMonitor.app with the SAME bundle id as
# the installed app, and every dev bundle that is launched or `lsregister -f`'d
# stays registered after its folder is deleted, so Spotlight/Finder show
# duplicate "AIMonitor" apps. Idempotent; macOS-only.
#
# Never touches /Applications, the Homebrew Caskroom, or the aimonitor daemon.

set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "dev-clean.sh: macOS-only" >&2
    exit 1
fi

LS=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister
INSTALLED=/Applications/AIMonitor.app
BUNDLE_ID=dev.aimonitor.AIMonitor
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEV_APP="$REPO_ROOT/build/AIMonitor.app"

# Hard exclusion: installed copies are never quit, unregistered or deleted.
is_protected() {
    case "$1" in
        /Applications | /Applications/* | /opt/homebrew/Caskroom | /opt/homebrew/Caskroom/*) return 0 ;;
        *) return 1 ;;
    esac
}

if is_protected "$DEV_APP"; then
    echo "dev-clean.sh: refusing to clean protected path $DEV_APP" >&2
    exit 1
fi

echo "==> quitting dev AIMonitor processes"
# Match only the app executable, so the aimonitor CLI/daemon is never a candidate.
while IFS= read -r line; do
    [[ "$line" =~ ^([0-9]+)\ (.*/Contents/MacOS/AIMonitor)(\ .*)?$ ]] || continue
    pid="${BASH_REMATCH[1]}"
    exe="${BASH_REMATCH[2]}"
    if is_protected "$exe"; then
        continue
    fi
    echo "    kill $pid $exe"
    kill "$pid" 2>/dev/null || true
done < <(pgrep -fl 'AIMonitor\.app' || true)

echo "==> removing $DEV_APP"
rm -rf "$DEV_APP"

echo "==> unregistering stale LaunchServices entries"
while IFS= read -r p; do
    [[ -n "$p" ]] || continue
    if is_protected "$p"; then
        continue
    fi
    echo "    lsregister -u $p"
    "$LS" -u "$p"
done < <("$LS" -dump | grep -E '^\s*path:.*AIMonitor\.app' | sed -E 's/^[[:space:]]*path:[[:space:]]*//; s/[[:space:]]+\(0x[0-9a-f]+\)$//' | sort -u || true)

if [[ -d "$INSTALLED" ]]; then
    "$LS" -f "$INSTALLED"
fi

echo "==> remaining registrations"
"$LS" -dump | grep -E '^\s*path:.*AIMonitor\.app' | sort -u || true
echo "==> mdfind $BUNDLE_ID"
mdfind "kMDItemCFBundleIdentifier == '$BUNDLE_ID'" || true
