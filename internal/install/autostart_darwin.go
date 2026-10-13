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
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
    <!-- Both streams go to one unified log file. The daemon logs all
         levels (INFO/WARN/ERROR) via slog to this same file; these
         redirects additionally capture raw Go-runtime panics. -->
    <key>StandardOutPath</key>
    <string>%[3]s/aimonitor.daemon.log</string>
    <key>StandardErrorPath</key>
    <string>%[3]s/aimonitor.daemon.log</string>
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

	plist := []byte(fmt.Sprintf(plistTemplate, LaunchAgentLabel, binaryPath, logDir))
	existing, _ := os.ReadFile(plistPath)
	unchanged := bytes.Equal(existing, plist)
	if !unchanged {
		if err := os.WriteFile(plistPath, plist, 0o644); err != nil {
			return fmt.Errorf("write plist: %w", err)
		}
	}

	uid := strconv.Itoa(os.Getuid())
	domain := "gui/" + uid
	target := domain + "/" + LaunchAgentLabel

	// Same plist and already loaded: nothing to apply. Bouncing a healthy
	// daemon here is what left it stopped when the widget's "Start daemon"
	// button was pressed on a daemon that was actually running.
	if unchanged && serviceLoaded(target) {
		return nil
	}

	// Bootout first so the bootstrap below picks up any plist changes
	// (path move, log path rename, etc.). Ignore the "service not
	// loaded yet" error on first run.
	_, _ = runLaunchctl("bootout", target)
	waitUnloaded(target)

	var out []byte
	var bootErr error
	for attempt := 0; attempt < bootstrapAttempts; attempt++ {
		if out, bootErr = runLaunchctl("bootstrap", domain, plistPath); bootErr == nil {
			return nil
		}
		if !bootstrapInProgress(bootErr, out) {
			break
		}
		pause(bootstrapRetryDelay)
	}
	return fmt.Errorf("launchctl bootstrap: %w (%s)", bootErr, string(out))
}

const (
	// bootout returns before launchd has finished removing the service, so a
	// bootstrap issued straight after fails with "37: Operation already in
	// progress" and leaves nothing running. Wait for the removal, and retry
	// the bootstrap if it still races.
	unloadWaitAttempts  = 50
	unloadPollInterval  = 200 * time.Millisecond
	bootstrapAttempts   = 10
	bootstrapRetryDelay = 500 * time.Millisecond
	launchctlEALREADY   = 37
	launchctlInProgress = "Operation already in progress"
)

// runLaunchctl and pause are seams for tests, which must not touch the
// developer's real launchd domain.
var (
	runLaunchctl = func(args ...string) ([]byte, error) {
		return exec.Command("launchctl", args...).CombinedOutput()
	}
	pause = time.Sleep
)

func serviceLoaded(target string) bool {
	_, err := runLaunchctl("print", target)
	return err == nil
}

func waitUnloaded(target string) {
	for i := 0; i < unloadWaitAttempts && serviceLoaded(target); i++ {
		pause(unloadPollInterval)
	}
}

func bootstrapInProgress(err error, out []byte) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == launchctlEALREADY {
		return true
	}
	return strings.Contains(string(out), launchctlInProgress)
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
	_, _ = runLaunchctl("bootout", target)

	if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

// IsAutostartEnabled reports whether launchctl currently knows about
// our LaunchAgent in the user's GUI domain. Cheap (~10ms).
func IsAutostartEnabled() (bool, error) {
	// `launchctl print` exits non-zero when the service isn't loaded; that
	// is a clean "false", not an error.
	return serviceLoaded("gui/" + strconv.Itoa(os.Getuid()) + "/" + LaunchAgentLabel), nil
}

func defaultLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Logs", "aimonitor"), nil
}
