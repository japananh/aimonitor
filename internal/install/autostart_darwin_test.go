//go:build darwin

package install

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover ONLY the pure path-computation + template-rendering
// helpers on macOS. The side-effecting functions (EnableAutostart,
// DisableAutostart, IsAutostartEnabled) shell out to /usr/bin/launchctl and
// write into the real ~/Library/LaunchAgents; calling them from a test would
// mutate the developer's launchd domain and home dir, so they are
// deliberately NOT exercised here (OS-only / side-effecting ceiling). The
// Linux systemd path lives behind a separate build tag and has its own
// OS-only ceiling.

func TestLaunchAgentPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := LaunchAgentPath()
	if err != nil {
		t.Fatalf("LaunchAgentPath: %v", err)
	}
	want := filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
	if got != want {
		t.Errorf("LaunchAgentPath = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, ".plist") {
		t.Errorf("path should end in .plist, got %q", got)
	}
}

func TestDefaultLogDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := defaultLogDir()
	if err != nil {
		t.Fatalf("defaultLogDir: %v", err)
	}
	want := filepath.Join(home, "Library", "Logs", "aimonitor")
	if got != want {
		t.Errorf("defaultLogDir = %q, want %q", got, want)
	}
}

func TestPlistTemplate_Renders(t *testing.T) {
	// Render the same way EnableAutostart does, but without writing it.
	binaryPath := "/usr/local/bin/aimonitor"
	logDir := "/Users/x/Library/Logs/aimonitor"
	plist := fmt.Sprintf(plistTemplate, LaunchAgentLabel, binaryPath, logDir)

	// Label must be the bundle-id-style identifier.
	if !strings.Contains(plist, "<string>"+LaunchAgentLabel+"</string>") {
		t.Errorf("plist missing Label %q:\n%s", LaunchAgentLabel, plist)
	}
	// ProgramArguments must invoke `<binary> daemon run`.
	for _, frag := range []string{
		"<string>" + binaryPath + "</string>",
		"<string>daemon</string>",
		"<string>run</string>",
	} {
		if !strings.Contains(plist, frag) {
			t.Errorf("plist missing ProgramArguments fragment %q:\n%s", frag, plist)
		}
	}
	// Both stdout and stderr redirect to the same unified log file.
	logFile := logDir + "/aimonitor.daemon.log"
	if strings.Count(plist, "<string>"+logFile+"</string>") != 2 {
		t.Errorf("plist should reference %q twice (stdout+stderr):\n%s", logFile, plist)
	}
	// Sanity: well-formed plist envelope.
	if !strings.HasPrefix(plist, "<?xml") || !strings.Contains(plist, "</plist>") {
		t.Errorf("plist envelope malformed:\n%s", plist)
	}
	// RunAtLoad / KeepAlive keep the daemon alive across boots.
	for _, key := range []string{"RunAtLoad", "KeepAlive"} {
		if !strings.Contains(plist, "<key>"+key+"</key>") {
			t.Errorf("plist missing <key>%s</key>", key)
		}
	}
}

func TestLaunchAgentLabel(t *testing.T) {
	// Pinned: must match the Label written into the plist; a drift here
	// would orphan the launchd service.
	if LaunchAgentLabel != "dev.aimonitor.daemon" {
		t.Errorf("LaunchAgentLabel = %q, want dev.aimonitor.daemon", LaunchAgentLabel)
	}
}
