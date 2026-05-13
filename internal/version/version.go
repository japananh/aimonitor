// Package version holds the build-time version string for aimonitor.
package version

// Version is injected at build time via -ldflags "-X .../version.Version=...".
var Version = "dev"
