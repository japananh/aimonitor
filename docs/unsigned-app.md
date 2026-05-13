# First-open workaround for the unsigned macOS app (v1.0.0-beta)

`AIMonitor.app` in v1.0.0-beta is **unsigned** — saving the $99/yr Apple Developer Program fee until we have non-developer users. macOS Gatekeeper will refuse to open it the first time. This is a one-time per-install workaround.

## Option A — right-click → Open

1. In Finder, navigate to `/Applications/AIMonitor.app`.
2. Right-click (or Control-click) the app → **Open**.
3. macOS will warn that the app is from an unidentified developer. Click **Open** again.

You will not see this prompt on subsequent launches.

## Option B — strip the quarantine attribute

If you prefer the terminal:

```sh
xattr -dr com.apple.quarantine /Applications/AIMonitor.app
open /Applications/AIMonitor.app
```

This removes the quarantine extended attribute that triggers Gatekeeper. Once stripped, the app launches normally.

## Why not Homebrew Cask's `--no-quarantine`?

Before 2020, casks bypassed the quarantine attribute by default. Modern Homebrew does not, so even `brew install --cask japananh/tap/aimonitor` results in a quarantined app. The two workarounds above are still required.

## When will this go away?

v1.1 will be notarized via the Apple Developer Program. The free path stays free: you can keep using the unsigned build indefinitely.
