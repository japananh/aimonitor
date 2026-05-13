//go:build linux

// autostart_linux drives systemd --user for the daemon. We deliberately
// use the per-user systemd manager (not system-wide) so aimonitor:
//   - never needs root,
//   - inherits the user's keyring (Secret Service runs in the user
//     session; a system-level unit would fail to read tokens),
//   - stops when the user logs out, which matches "background helper
//     that wakes when I'm at the keyboard."
//
// Unit file path: $XDG_CONFIG_HOME/systemd/user/aimonitor.service.

package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	// SystemdUnitName is the user-level service identifier. Must match
	// what's written inside the unit file's [Service] section (well,
	// the file name carries it; the unit itself names sections, not
	// itself).
	SystemdUnitName = "aimonitor.service"

	systemdUnitTemplate = `[Unit]
Description=aimonitor daemon (Claude account watcher + auto-switcher)
After=default.target

[Service]
Type=simple
ExecStart=%s daemon run
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
`
)

// LaunchAgentPath returns the systemd user unit file path. Name kept
// for cross-platform symmetry with the darwin sibling.
func LaunchAgentPath() (string, error) {
	dir, err := systemdUserDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SystemdUnitName), nil
}

// EnableAutostart writes the unit, reloads the user daemon, and enables
// + starts the service. Idempotent — re-enabling an already-running
// service is a no-op other than picking up unit changes.
func EnableAutostart(binaryPath string) error {
	if binaryPath == "" {
		bp, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate executable: %w", err)
		}
		binaryPath = bp
	}

	dir, err := systemdUserDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir systemd user dir: %w", err)
	}
	unitPath := filepath.Join(dir, SystemdUnitName)
	unit := fmt.Sprintf(systemdUnitTemplate, binaryPath)
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}

	if err := runSystemctl("daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctl("enable", "--now", SystemdUnitName); err != nil {
		return err
	}
	return nil
}

// DisableAutostart stops the unit, disables it, and removes the file.
// Missing unit + already-stopped service both yield nil.
func DisableAutostart() error {
	// `disable --now` stops as well; ignore "not loaded" failures.
	_ = runSystemctl("disable", "--now", SystemdUnitName)

	unitPath, err := LaunchAgentPath()
	if err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove unit: %w", err)
	}
	_ = runSystemctl("daemon-reload")
	return nil
}

// IsAutostartEnabled reports whether systemctl --user reports the unit
// as enabled. A unit can exist on disk but be disabled, so we check
// is-enabled, not is-active (which would only flag a running daemon).
func IsAutostartEnabled() (bool, error) {
	out, err := exec.Command("systemctl", "--user", "is-enabled", SystemdUnitName).Output()
	if err != nil {
		// is-enabled exits non-zero for disabled/missing services.
		// Distinguish "not enabled" (clean) from systemctl missing.
		// errors.As walks the wrap chain — robust if Go ever wraps
		// command exit errors at a higher level.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("systemctl is-enabled: %w", err)
	}
	state := string(out)
	return state == "enabled\n" || state == "static\n", nil
}

func runSystemctl(args ...string) error {
	all := append([]string{"--user"}, args...)
	cmd := exec.Command("systemctl", all...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %v: %w (%s)", args, err, string(out))
	}
	return nil
}

func systemdUserDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}
