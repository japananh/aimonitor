package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

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
