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

> **Gotcha to know before reading the steps below.** Goreleaser creates
> the GitHub Release as a **draft**. Draft releases on GitHub are NOT
> publicly downloadable — `releases/download/<tag>/<asset>` URLs return
> 404 to unauthenticated requests. So `brew install --cask aimonitor`
> will fail with `curl: (56) ... 404` until the draft is published, even
> though the cask in the tap is correct and points at real URLs.
>
> This means **publishing happens BEFORE `brew install` can be tested**.
> The recovery if the smoke test finds a bug is retag-as-`-beta.<n+1>`, not
> "edit the draft." See the Rollback section below.

1. Open the draft Release on GitHub. Verify the changelog reads cleanly;
   edit if needed.

2. (Optional) Smoke-test the artifacts *without* publishing, using the
   `gh` CLI's authenticated download path:

   ```bash
   mkdir -p /tmp/release-smoke && cd /tmp/release-smoke
   gh release download v1.0.0-beta.1 --repo japananh/aimonitor \
     --pattern '*darwin_universal*'
   tar -xzf aimonitor_*_darwin_universal.tar.gz
   file aimonitor                         # should be: Mach-O universal (arm64 + x86_64)
   lipo -info aimonitor                   # should list both arches
   ls -la AIMonitor.app/Contents/         # should have Info.plist + MacOS/ + PkgInfo
   ./aimonitor --version                  # should print v1.0.0-beta.1
   ```

   This verifies the artifact is structurally correct without exposing
   it to the world. It does NOT test the `brew install` path — that
   requires publish.

3. **Publish the Release.** Until you do this, `brew install --cask
   aimonitor` returns 404. After this, it works for everyone.

4. Smoke-test the *published* release on a real machine: `brew install`/`upgrade`, daemon starts, widget shows usage, a manual switch works, `aimonitor doctor` is clean.
5. If both checklists pass: announce. The release is done.

6. If a checklist fails: see Rollback.

## Rollback

If a published release is bad:

1. Delete the GitHub Release (Releases page → click release → Delete).
   This removes the public download URLs immediately.
2. (Optional) Edit the `homebrew-tap` `Casks/aimonitor.rb` back to a
   previous good version. Cask installs only happen on demand, so the
   exposure window is short — anyone who didn't run `brew install`
   between the publish and the delete is unaffected. If this is the
   very first release, you can simply delete `Casks/aimonitor.rb`
   from the tap; goreleaser will write a fresh one on the next release.
3. Fix the bug on `main`. Confirm CI is green.
4. **Retag with a higher version**, never reuse a tag:

   ```bash
   git tag -a v1.0.0-beta.2 -m "v1.0.0-beta.2"
   git push origin v1.0.0-beta.2
   ```

   The release workflow re-runs cleanly; the new cask overwrites the
   old one in the tap. Don't bother trying to delete the bad git tag
   from the remote — leaving it there gives an audit trail without
   confusing anyone (it's just an unreleased tag).

## Notarization (deferred to v1.1)

v1.0.0-beta ships unsigned. End users must clear the quarantine xattr
manually on first run (`docs/unsigned-app.md`). The cask `caveats`
block surfaces this prominently.

For v1.1 we'll add a `gon`-style notarization step to the release
workflow plus a Developer ID Application certificate. That's out of
scope for v1 because (a) the cert costs $99/yr and (b) notarization
slows the release loop materially during beta.
