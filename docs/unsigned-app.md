# First-open workaround for the unsigned macOS app

`AIMonitor.app` is currently **unsigned** — saving the $99/yr Apple Developer Program fee until we have non-developer users. macOS Gatekeeper refuses to open it until the quarantine flag is cleared. You'll need this once per *install* — and because `brew upgrade --cask` (and any reinstall) lays down a fresh `.app`, **it recurs after every upgrade** (see [After an upgrade or reinstall](#after-an-upgrade-or-reinstall)).

Symptoms vary by macOS version: *"AIMonitor is damaged and can't be opened"*, *"can't be opened because Apple cannot check it for malicious software"*, or *"unidentified developer"*. All are the same quarantine flag — **not** a corrupt download, so reinstalling or `aimonitor uninstall --purge` won't help (and `--purge` deletes your saved account logins).

## Option A — right-click → Open

1. In Finder, navigate to `/Applications/AIMonitor.app`.
2. Right-click (or Control-click) the app → **Open**.
3. macOS will warn that the app is from an unidentified developer. Click **Open** again.

You will not see this prompt again on this install — but the next upgrade or reinstall brings it back (see below).

## Option B — strip the quarantine attribute

If you prefer the terminal:

```sh
xattr -dr com.apple.quarantine /Applications/AIMonitor.app
open /Applications/AIMonitor.app
```

This removes the quarantine extended attribute that triggers Gatekeeper. Once stripped, the app launches normally.

## After an upgrade or reinstall

`brew upgrade --cask aimonitor` — and any reinstall — replaces `/Applications/AIMonitor.app` with a fresh, quarantined copy, so it refuses to open again exactly as on first install. That is expected; re-run the fix:

```sh
xattr -dr com.apple.quarantine /Applications/AIMonitor.app
open /Applications/AIMonitor.app
```

Re-running the one-line installer (`packaging/macos/install.sh`) does this for you. The CLI (`aimonitor …`) is never quarantined and keeps working across upgrades — only the `.app` needs re-clearing. There is no need to reinstall, and **never** `aimonitor uninstall --purge` (it deletes your saved account logins).

## Why not Homebrew Cask's `--no-quarantine`?

Before 2020, casks bypassed the quarantine attribute by default. Modern Homebrew does not, so even `brew install --cask japananh/tap/aimonitor` results in a quarantined app. The two workarounds above are still required.

## When will this go away?

A future release will be notarized via the Apple Developer Program. The free path stays free: you can keep using the unsigned build indefinitely.
