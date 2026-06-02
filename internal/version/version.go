// Package version holds the build-time version string for aimonitor.
package version

// Version is injected at build time via -ldflags "-X .../version.Version=...".
// Defaults to "dev" for `go run` / `go build` without ldflags.
var Version = "dev"

// Commit is the short git SHA of the build, injected by goreleaser.
var Commit = "none"

// BuildDate is the RFC3339 build timestamp, injected by goreleaser.
var BuildDate = "unknown"
