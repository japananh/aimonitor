//go:build darwin

// Package install wires platform-specific autostart helpers for the
// `aimonitor` daemon (NOT the menu bar app — the .app self-registers
// via SMAppService from Swift). On macOS this means dropping a
// LaunchAgent plist into ~/Library/LaunchAgents and bootstrapping it
// into the user's GUI launchd domain.
//
// Why a Go helper and not a Swift one: `aimonitor config set autostart
// true` is a CLI-only flow (no widget required). A user setting up the
// daemon over SSH or in a headless context never opens the GUI.
package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const (
	// LaunchAgentLabel is the bundle-id-style identifier launchd uses
	// to refer to our daemon. Must match the Label key inside the
	// plist we write.
	LaunchAgentLabel = "dev.aimonitor.daemon"

	plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%[1]s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%[2]s</string>
        <string>daemon</string>
        <string>run</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%[3]s/aimonitor.daemon.out.log</string>
    <key>StandardErrorPath</key>
    <string>%[3]s/aimonitor.daemon.err.log</string>
</dict>
</plist>
`
)

// LaunchAgentPath returns the canonical per-user plist path. Exposed so
// tests + `aimonitor doctor` can sanity-check the file's presence.
func LaunchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist"), nil
}

// EnableAutostart writes (or rewrites) the LaunchAgent plist for the
// given aimonitor binary and bootstraps it into the user's launchd
// GUI domain. Idempotent — bootstrapping an already-loaded label is a
// no-op other than refreshing the plist.
func EnableAutostart(binaryPath string) error {
	if binaryPath == "" {
		bp, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate executable: %w", err)
		}
		binaryPath = bp
	}

	plistPath, err := LaunchAgentPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("mkdir LaunchAgents: %w", err)
	}

	logDir, err := defaultLogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("mkdir log dir: %w", err)
	}

	plist := fmt.Sprintf(plistTemplate, LaunchAgentLabel, binaryPath, logDir)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	uid := strconv.Itoa(os.Getuid())
	target := "gui/" + uid

	// Bootout first so the bootstrap below picks up any plist changes
	// (path move, log path rename, etc.). Ignore the "service not
	// loaded yet" error on first run.
	_ = exec.Command("launchctl", "bootout", target+"/"+LaunchAgentLabel).Run()

	bootstrap := exec.Command("launchctl", "bootstrap", target, plistPath)
	out, err := bootstrap.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w (%s)", err, string(out))
	}
	return nil
}

// DisableAutostart unloads the LaunchAgent and removes its plist.
// Idempotent — missing plist + not-loaded agent both yield nil.
func DisableAutostart() error {
	plistPath, err := LaunchAgentPath()
	if err != nil {
		return err
	}

	uid := strconv.Itoa(os.Getuid())
	target := "gui/" + uid + "/" + LaunchAgentLabel
	// Tolerate "not loaded" on first call.
	_ = exec.Command("launchctl", "bootout", target).Run()

	if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

// IsAutostartEnabled reports whether launchctl currently knows about
// our LaunchAgent in the user's GUI domain. Cheap (~10ms).
func IsAutostartEnabled() (bool, error) {
	uid := strconv.Itoa(os.Getuid())
	target := "gui/" + uid + "/" + LaunchAgentLabel
	cmd := exec.Command("launchctl", "print", target)
	if err := cmd.Run(); err != nil {
		// `launchctl print` exits non-zero when the service isn't
		// loaded; treat that as a clean "false" rather than an error.
		return false, nil
	}
	return true, nil
}

func defaultLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Logs", "aimonitor"), nil
}
