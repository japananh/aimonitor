package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/install"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newUninstallCmd() *cobra.Command {
	var purge, yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall aimonitor (use --purge to also remove data + Keychain entries)",
		Long: `Disable autostart and (with --purge) also drop every aimonitor-managed
piece of state: the SQLite database, the config YAML, and every
aimonitor-namespaced keyring entry. The user's original
Claude Code-credentials keyring slot is left UNTOUCHED so existing
Claude Code logins continue to work after uninstall.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUninstall(cmd, purge, yes)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove SQLite DB, config YAML, and aimonitor keyring entries")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the --purge confirmation prompt")
	return cmd
}

// runUninstall is exported via the command but split out for testability.
// The actual `brew uninstall` step is the user's job — this only cleans
// up the runtime state Homebrew doesn't know about (LaunchAgent, DB,
// keyring entries).
func runUninstall(cmd *cobra.Command, purge, yes bool) error {
	ctx := context.Background()
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	// Step 1: always disable autostart. If it was never enabled,
	// platform helpers tolerate missing units silently.
	if err := install.DisableAutostart(); err != nil && !errors.Is(err, install.ErrAutostartUnsupported) {
		fmt.Fprintf(errOut, "warning: disable autostart: %v\n", err)
	} else {
		fmt.Fprintln(out, "autostart disabled")
	}

	// Step 1b: belt-and-suspenders. `launchctl bootout` only stops
	// processes launchd is currently tracking. Two things survive it:
	//   - A daemon started manually (`aimonitor daemon run` in a
	//     terminal), or one launchd lost track of during throttle
	//     backoff.
	//   - The AIMonitor.app menu-bar widget — it self-registers via
	//     SMAppService, not via the LaunchAgent plist we manage.
	// Both keep holding open file handles + Keychain prompts. Send
	// SIGTERM to each, then wait briefly to let them flush state.
	if n := terminateOrphanDaemons(errOut); n > 0 {
		fmt.Fprintf(out, "terminated %d orphan daemon process(es)\n", n)
	}

	if !purge {
		fmt.Fprintln(out, "Done. State preserved. Re-run with --purge to also drop the database and keyring entries.")
		return nil
	}

	// --purge: confirm unless --yes was passed. Stdin is a TTY in
	// normal use; if not, refuse rather than silently nuke data.
	if !yes {
		fmt.Fprint(out, "This will delete the aimonitor database, config, and every aimonitor-namespaced keyring entry.\nType 'PURGE' to confirm: ")
		var answer string
		if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil || answer != "PURGE" {
			return errors.New("aborted: confirmation not received")
		}
	}

	// Step 2: drop every aimonitor stash from the keyring. We need
	// the account list before we drop the DB, so order matters.
	dbPath, err := store.DefaultPath()
	if err != nil {
		return fmt.Errorf("resolve DB path: %w", err)
	}
	if _, statErr := os.Stat(dbPath); statErr == nil {
		s, err := store.Open(dbPath)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		accounts, err := s.ListAccounts(ctx)
		_ = s.Close()
		if err != nil {
			fmt.Fprintf(errOut, "warning: list accounts: %v\n", err)
		}
		for _, a := range accounts {
			if err := claude.DeleteStash(ctx, a.KeyringRef); err != nil {
				fmt.Fprintf(errOut, "warning: delete stash %q: %v\n", a.Label, err)
			}
		}
		fmt.Fprintf(out, "removed %d keyring stash entries\n", len(accounts))

		// Step 3: drop the DB + WAL/SHM sidecars.
		for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(errOut, "warning: remove %s: %v\n", p, err)
			}
		}
		fmt.Fprintln(out, "SQLite database removed")
	} else {
		fmt.Fprintln(out, "no database to remove")
	}

	// Step 4: drop the config YAML. Leave the parent directory alone
	// (other tools may share $XDG_CONFIG_HOME/aimonitor in the future).
	cfgPath, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(errOut, "warning: resolve config path: %v\n", err)
	} else if err := os.Remove(cfgPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(errOut, "warning: remove config: %v\n", err)
	} else {
		fmt.Fprintln(out, "config YAML removed")
	}

	fmt.Fprintln(out, "Done. Claude Code-credentials keyring slot left untouched.")
	return nil
}

// terminateOrphanDaemons SIGTERMs any `aimonitor daemon` process AND
// any running AIMonitor.app menu-bar process that the current
// uninstall didn't manage to stop via launchctl. Returns the number
// of processes signaled. Skips this process's own PID so we don't
// kill the uninstall command mid-flight.
//
// Two patterns:
//   - "aimonitor daemon" — the headless CLI watcher, normally
//     managed by launchctl. Survives bootout if started manually or
//     if launchd lost track of it during throttle backoff.
//   - "AIMonitor.app/Contents/MacOS/AIMonitor" — the Swift menu-bar
//     widget. Not under launchctl (it self-registers via
//     SMAppService), so the launchctl bootout step in
//     install.DisableAutostart cannot stop it.
//
// pgrep rather than /proc parsing because Darwin has no procfs.
func terminateOrphanDaemons(errOut interface{ Write([]byte) (int, error) }) int {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return 0
	}

	patterns := []string{"aimonitor daemon"}
	if runtime.GOOS == "darwin" {
		patterns = append(patterns, "AIMonitor.app/Contents/MacOS/AIMonitor")
	}

	self := os.Getpid()
	count := 0
	for _, pattern := range patterns {
		output, err := exec.Command("pgrep", "-f", pattern).Output()
		if err != nil {
			// pgrep exit 1 = no match; treat as success.
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line == "" {
				continue
			}
			var pid int
			if _, err := fmt.Sscanf(line, "%d", &pid); err != nil || pid <= 0 || pid == self {
				continue
			}
			proc, err := os.FindProcess(pid)
			if err != nil {
				continue
			}
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				// On macOS FindProcess always succeeds; Signal fails
				// if the PID belongs to another user or vanished.
				// Either way, not fatal — log and move on.
				fmt.Fprintf(errOut, "warning: signal pid %d: %v\n", pid, err)
				continue
			}
			count++
		}
	}

	// Give signaled processes a moment to actually exit before the
	// caller reports success. 500ms is enough for our daemon's
	// signal handler to flush + close DB, and short enough not to
	// be annoying when run interactively.
	if count > 0 {
		time.Sleep(500 * time.Millisecond)
	}
	return count
}
