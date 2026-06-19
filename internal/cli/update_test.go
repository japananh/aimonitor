package cli

import (
	"strings"
	"testing"
)

// The self-update script MUST relaunch the app after the upgrade — brew quits
// it mid-upgrade and nothing else reopens it, so a missing relaunch leaves the
// user with no menu-bar app (the bug this guards against). Also assert it
// clears the Gatekeeper quarantine first (the cask is unsigned) and runs the
// cask upgrade with the given brew path.
func TestUpgradeScript_RelaunchesApp(t *testing.T) {
	s := upgradeScript("/opt/homebrew/bin/brew")

	for _, want := range []string{
		`"/opt/homebrew/bin/brew" upgrade --cask aimonitor`,
		"/usr/bin/xattr -dr com.apple.quarantine /Applications/AIMonitor.app",
		"/usr/bin/open /Applications/AIMonitor.app",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("upgrade script missing %q\n--- script ---\n%s", want, s)
		}
	}

	// The relaunch must come AFTER the upgrade, not before.
	if up, open := strings.Index(s, "upgrade --cask"), strings.Index(s, "/usr/bin/open"); up < 0 || open < up {
		t.Errorf("relaunch (open) must follow the upgrade: upgrade@%d open@%d", up, open)
	}
}
