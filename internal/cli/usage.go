package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

func newUsageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Inspect or refresh per-account usage",
	}
	cmd.AddCommand(newUsageRefreshCmd())
	return cmd
}

func newUsageRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh [label]",
		Short: "Fetch current 5h/7d usage for inactive accounts (all, or one by label)",
		Long: `Fetch and store current usage, refreshing an account's stashed token if
it has expired.

With no argument, refreshes EVERY account, including the active one
(best-effort — per-account failures are reported but don't fail the run).

With a <label>, refreshes just that account and FAILS (non-zero exit) if
the fetch can't complete, so the caller sees the error.

The active account is refreshed through the Switcher's locked live path so
its stash stays in sync with the live keychain slot.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				if len(args) == 1 {
					return runUsageRefreshOne(ctx, cmd, s, p, args[0])
				}
				return runUsageRefresh(ctx, cmd, s, p)
			})
		},
	}
}

// runUsageRefreshOne refreshes a single account by label and returns the
// error (non-zero exit) on failure so the widget can show it per-row.
func runUsageRefreshOne(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider, label string) error {
	acct, err := s.GetAccountByLabel(ctx, label)
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			return fmt.Errorf("no account labeled %q (see `aimonitor list`)", label)
		}
		return err
	}

	cc, _ := claudeconfig.New()
	active, found, _ := daemon.ResolveActiveAccount(ctx, s, p, cc)
	isActive := found && active.ID == acct.ID

	fetcher := claude.NewUsageFetcher()
	var lim provider.Limits
	if isActive {
		// The active account's stash must stay in sync with the live slot,
		// so route through the Switcher's locked live-refresh path rather
		// than the stash-rotating RefreshAccountUsage.
		lim, err = daemon.RefreshActiveUsage(ctx, s, daemon.NewSwitcher(s, p), fetcher, acct)
	} else {
		lim, err = daemon.RefreshAccountUsage(ctx, s, fetcher, claude.NewTokenRefresher(), acct)
	}
	if err != nil {
		return fmt.Errorf("refresh %q: %w", label, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s 5h %.0f%%  7d %.0f%%\n", label, lim.FiveHourPct, lim.SevenDayPct)
	return nil
}

func runUsageRefresh(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider) error {
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No accounts to refresh.")
		return nil
	}

	// Identify the active account so it can be refreshed through the safe
	// live-refresh path (which keeps its stash in sync with the live slot)
	// rather than the stash-rotating RefreshAccountUsage.
	cc, _ := claudeconfig.New()
	activeID := int64(-1)
	if active, found, rErr := daemon.ResolveActiveAccount(ctx, s, p, cc); rErr == nil && found {
		activeID = active.ID
	}

	fetcher := claude.NewUsageFetcher()
	refresher := claude.NewTokenRefresher()
	sw := daemon.NewSwitcher(s, p)

	var refreshed, failed int
	for _, acct := range accounts {
		var lim provider.Limits
		var err error
		if acct.ID == activeID {
			lim, err = daemon.RefreshActiveUsage(ctx, s, sw, fetcher, acct)
		} else {
			lim, err = daemon.RefreshAccountUsage(ctx, s, fetcher, refresher, acct)
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%-18s failed: %v\n", acct.Label, err)
			failed++
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-18s 5h %.0f%%  7d %.0f%%\n", acct.Label, lim.FiveHourPct, lim.SevenDayPct)
		refreshed++
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n%d refreshed, %d failed.\n", refreshed, failed)
	return nil
}
