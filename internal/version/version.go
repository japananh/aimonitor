package version

// Version is injected at build time via -ldflags "-X .../version.Version=...".
var Version = "dev"
