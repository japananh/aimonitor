package cli

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPurgeExtraPaths(t *testing.T) {
	paths := purgeExtraPaths()
	if len(paths) == 0 {
		t.Fatal("purgeExtraPaths returned nothing")
	}

	joined := strings.Join(paths, "\n")
	// The switch lock and post-swap dir are removed on every platform.
	if !strings.Contains(joined, filepath.Join(".aimonitor-lock")) {
		t.Errorf("missing switch lock in %v", paths)
	}
	mustHaveSuffix(t, paths, ".aimonitor")

	if runtime.GOOS == "darwin" {
		mustHaveSuffix(t, paths, filepath.Join("Library", "Logs", "aimonitor"))
		mustHaveSuffix(t, paths, filepath.Join("Library", "Preferences", "dev.aimonitor.AIMonitor.plist"))
	}

	// Never list the shared Claude Code keychain or anything resembling it.
	for _, p := range paths {
		if strings.Contains(p, "Claude Code-credentials") {
			t.Errorf("purge must never touch the shared Claude slot, got %q", p)
		}
	}
}

func mustHaveSuffix(t *testing.T, paths []string, suffix string) {
	t.Helper()
	for _, p := range paths {
		if strings.HasSuffix(p, suffix) {
			return
		}
	}
	t.Errorf("no path ends with %q in %v", suffix, paths)
}
