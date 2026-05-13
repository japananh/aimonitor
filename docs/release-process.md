# Release process

How a tag becomes a published v1.x release.

## Prerequisites (one-time, per maintainer)

1. Push permission to `japananh/aimonitor` and `japananh/homebrew-tap`.
2. A GitHub personal-access token with `repo` scope, stored as
   `HOMEBREW_TAP_TOKEN` in the **aimonitor** repo's Actions secrets.
   (The default `GITHUB_TOKEN` can only write to the repo it runs in;
   cross-repo pushes to the tap need a PAT.)
3. The `japananh/homebrew-tap` repo must exist with a `main` branch
   (one initial commit is enough — goreleaser writes the cask into
   `Casks/aimonitor.rb`).

## Cutting a release

```bash
# Confirm the local + remote main are in sync and CI is green.
git fetch origin
git checkout main
git pull --ff-only

# Confirm the version in the homebrew template matches the tag you're
# about to push. If they don't, update packaging/homebrew/aimonitor.rb
# and the Info.plist CFBundleShortVersionString, commit, push.

# Tag and push.
git tag -a v1.0.0-beta.1 -m "v1.0.0-beta.1"
git push origin v1.0.0-beta.1
```

The `release.yml` workflow:

1. Checks out main + the tag (deep fetch for changelog generation).
2. Runs `bash scripts/bundle-app.sh` to produce `build/AIMonitor.app`.
3. Invokes `goreleaser release --clean`, which:
   - Cross-compiles the CLI for `darwin/{amd64,arm64}` (CGO=1) and
     `linux/{amd64,arm64}` (CGO=0).
   - Fuses the two darwin binaries into a single universal Mach-O.
   - Archives each target as a `.tar.gz` (the Mac archive includes
     `AIMonitor.app/`).
   - Computes SHA-256s.
   - Creates a **draft** GitHub Release on `japananh/aimonitor` with
     all artifacts attached.
   - Pushes an updated `Casks/aimonitor.rb` to `japananh/homebrew-tap`.

Goreleaser flags it as a prerelease automatically because the tag
contains `-beta`.

## After the workflow

1. Open the draft Release on GitHub.
2. Verify the changelog reads cleanly.
3. **Manually test** the artifacts: download the Mac tarball, extract,
   run `xattr -dr com.apple.quarantine AIMonitor.app && open AIMonitor.app`.
4. Publish the Release.
5. Verify the cask landed: `brew tap japananh/tap && brew install --cask aimonitor`.

## Rollback

If a release is bad:

1. Mark the Release as draft (or delete it) on GitHub.
2. Force-push the `homebrew-tap` `Casks/aimonitor.rb` back to the
   previous good version. Cask user installs only happen on demand, so
   no one has the bad version unless they ran `brew install` between
   the publish and the rollback.
3. Re-tag with a higher patch number (`v1.0.0-beta.2`) — do not reuse
   a tag.

## Notarization (deferred to v1.1)

v1.0.0-beta ships unsigned. End users must clear the quarantine xattr
manually on first run (`docs/unsigned-app.md`). The cask `caveats`
block surfaces this prominently.

For v1.1 we'll add a `gon`-style notarization step to the release
workflow plus a Developer ID Application certificate. That's out of
scope for v1 because (a) the cert costs $99/yr and (b) notarization
slows the release loop materially during beta.
