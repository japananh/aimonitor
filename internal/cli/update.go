package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/updater"
	"github.com/japananh/aimonitor/internal/version"
)

func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for and install aimonitor updates",
	}
	cmd.AddCommand(newUpdateCheckCmd(), newUpdateInstallCmd())
	return cmd
}

func newUpdateCheckCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check GitHub for a newer release (no install, no token cost)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := (&updater.Checker{}).CheckLatest(cmd.Context(), version.Version)
			if err != nil {
				return err
			}
			if asJSON {
				out, mErr := json.Marshal(info)
				if mErr != nil {
					return mErr
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			if info.Available {
				fmt.Fprintf(cmd.OutOrStdout(), "Update available: %s → %s\n%s\n",
					info.Current, info.Latest, info.URL)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Up to date (%s).\n", info.Current)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the check result as JSON")
	return cmd
}

func newUpdateInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Upgrade aimonitor via Homebrew (runs detached, in the background)",
		Long: `Spawn a detached background job that refreshes the Homebrew tap and runs
'brew upgrade --cask aimonitor'.

It runs detached on purpose: 'brew upgrade --cask' quits the running app
mid-upgrade, so the upgrade can't be performed by a process the app owns —
it would be killed partway. The detached job survives the app quitting, and
when brew finishes it clears the Gatekeeper quarantine and relaunches the
widget itself (the cask postflight only registers daemon autostart, and the
unsigned app would otherwise stay quit). Progress is logged to
~/Library/Logs/aimonitor/update.log.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			brew, err := findBrew()
			if err != nil {
				return fmt.Errorf("%w\nInstall manually from %s", err, updater.HTMLURL)
			}
			logPath, err := updateLogPath()
			if err != nil {
				return err
			}
			if err := spawnDetachedUpgrade(brew, logPath); err != nil {
				return fmt.Errorf("start background upgrade: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Upgrade started in the background. The app will quit and relaunch when it completes.\nLog: %s\n",
				logPath)
			return nil
		},
	}
}

// findBrew locates the Homebrew executable. GUI apps inherit a minimal PATH
// without /opt/homebrew/bin, so we probe the known install locations
// (Apple Silicon, then Intel) by absolute path rather than relying on PATH.
func findBrew() (string, error) {
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath("brew"); err == nil {
		return p, nil
	}
	return "", errors.New("could not find Homebrew (looked in /opt/homebrew/bin, /usr/local/bin, and PATH)")
}

func updateLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, "Library", "Logs", "aimonitor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create log dir: %w", err)
	}
	return filepath.Join(dir, "update.log"), nil
}

// spawnDetachedUpgrade launches the tap-refresh + cask-upgrade in a new
// session (Setsid) so it outlives both this short-lived CLI process and the
// menu-bar app that `brew upgrade --cask` quits mid-run. We do not Wait —
// the child reparents to launchd and runs to completion on its own.
//
// The tap is pulled directly (fast, and the exact stale-tap fix needed so
// `brew upgrade` sees the just-published version); a pull failure falls back
// to `brew update`. brew is invoked by absolute path for the same minimal-PATH
// reason as findBrew.
func spawnDetachedUpgrade(brew, logPath string) error {
	script := upgradeScript(brew)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open update log: %w", err)
	}
	defer logFile.Close() // child dup'd the fd; parent can close

	c := exec.Command("/bin/bash", "-c", script)
	c.Stdout = logFile
	c.Stderr = logFile
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return c.Start() // detached: deliberately not Wait()
}

// upgradeScript builds the detached self-update shell script: refresh the tap,
// run the cask upgrade, then ALWAYS bring the menu-bar app back. The relaunch
// is the load-bearing part — brew quits the app mid-upgrade and nothing else
// reopens it (the cask postflight only sets daemon autostart, and the unsigned
// app is Gatekeeper-quarantined after a fresh download), so without this an
// upgrade — successful OR reverted — leaves the user with no app.
//
// Timestamps use the daemon's ISO-8601 local format so update.log reads
// consistently with aimonitor.daemon.log. RC is captured right after the
// upgrade (before the date call, which would otherwise reset $?).
func upgradeScript(brew string) string {
	return fmt.Sprintf(`
set +e
TS() { date +%%Y-%%m-%%dT%%H:%%M:%%S%%z; }
echo "=== aimonitor self-update $(TS) ==="
TAP="$(%[1]q --repository)/Library/Taps/japananh/homebrew-tap"
if [ -d "$TAP/.git" ]; then
  git -C "$TAP" pull --ff-only || %[1]q update
else
  %[1]q update
fi
%[1]q upgrade --cask aimonitor
RC=$?
# Reopen whatever is now installed — the new version on success, or the version
# brew reverted to on a failed upgrade — after clearing the Gatekeeper
# quarantine. Absolute paths: the detached job inherits a minimal PATH.
if [ -d /Applications/AIMonitor.app ]; then
  /usr/bin/xattr -dr com.apple.quarantine /Applications/AIMonitor.app 2>/dev/null
  /usr/bin/open /Applications/AIMonitor.app
  echo "relaunched app (xattr cleared)"
else
  echo "WARNING: /Applications/AIMonitor.app missing after upgrade (exit $RC) — not relaunched"
fi
echo "=== done $(TS) (exit $RC) ==="
`, brew)
}
