# Release process

How a tag becomes a published v1.x release. Maintainer reference — not
linked from the README.

## One-time setup

- Push access to `japananh/aimonitor` and `japananh/homebrew-tap`.
- A PAT with `repo` scope stored as `HOMEBREW_TAP_TOKEN` in this repo's
  Actions secrets. The default `GITHUB_TOKEN` can't push cross-repo to the
  tap, so writing the cask needs the PAT.
- `japananh/homebrew-tap` exists with a `main` branch (goreleaser writes
  `Casks/aimonitor.rb` into it).

## Cutting a release

```bash
git checkout main && git pull --ff-only     # main green and in sync
git tag -a v1.0.0 -m "v1.0.0" && git push origin v1.0.0
```

No files to bump first — the version is derived entirely from the git
tag at build time:

- **CLI:** `internal/version.Version` is set via goreleaser ldflags (`-X
  ...Version={{.Version}}`).
- **`.app`:** `scripts/bundle-app.sh` stamps `CFBundleShortVersionString`
  and `CFBundleVersion` from `git describe`.
- **Homebrew cask:** goreleaser regenerates `version`/`sha256`/URL into
  `japananh/homebrew-tap`.

The committed `ui/macos/Resources/Info.plist` and
`packaging/homebrew/aimonitor.rb` are reference templates; their literal
version strings intentionally never move (the plist sits at `1.1.0`), so
don't bump them expecting an effect.

`release.yml` then bundles `build/AIMonitor.app` and runs
`goreleaser release --clean`: cross-compiles the CLI (darwin + linux, both
arches, `CGO_ENABLED=0`), fuses a universal macOS binary, archives +
checksums everything, creates a **draft** GitHub Release, and pushes an
updated cask to the tap. Tags containing `-beta` are flagged prerelease
automatically.

## After the workflow

> **The draft trap.** Goreleaser leaves the Release as a **draft**, and
> draft assets return 404 to unauthenticated requests — so `brew install
> --cask aimonitor` fails with `curl: (56) ... 404` until you publish, even
> though the cask is already correct. Publishing therefore happens *before*
> the `brew install` path can be tested; if a later smoke test fails, the
> fix is a retag (see Rollback), never editing the draft.

1. Open the draft Release; check the changelog reads cleanly.
2. (Optional) Smoke-test artifacts without publishing via `gh release
   download <tag> --repo japananh/aimonitor` — confirm the binary is a
   universal Mach-O (`lipo -info`) and `./aimonitor --version` is right.
3. **Publish.** Only now does `brew install --cask aimonitor` work.
4. Smoke-test the published release on a real machine: `brew upgrade`,
   daemon starts, widget shows usage, a switch works, `doctor` is clean.
5. Pass → announce. Fail → Rollback.

## Rollback

Never reuse a tag.

1. Delete the GitHub Release — this kills the public download URLs at once.
2. Fix on `main`, confirm CI is green.
3. Retag with a higher version and push; the workflow re-runs and the new
   cask overwrites the old one in the tap. Leave the bad git tag in place —
   it's just an unreleased tag and gives an audit trail.

## Notarization

Still deferred — the `.app` ships unsigned to save the $99/yr Apple
Developer fee while the audience is developers. See
[`unsigned-app.md`](unsigned-app.md) for the first-open workaround and when
that changes.
