package cli

import (
	"context"
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
		Use:   "refresh",
		Short: "Fetch current 5h/7d usage for all inactive accounts",
		Long: `Fetch and store current usage for every account except the active one,
refreshing an account's stashed token if it has expired.

The active account is refreshed continuously by the daemon and is skipped
here — refreshing its stash out-of-band would desync it from the live
keychain slot. Use this to populate usage for accounts that have been idle
long enough for their cached usage (or token) to go stale.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				return runUsageRefresh(ctx, cmd, s, p)
			})
		},
	}
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

	// Identify the active account so we can skip it (its stash must not be
	// rotated out-of-band; the daemon keeps it fresh).
	cc, _ := claudeconfig.New()
	activeID := int64(-1)
	if active, found, rErr := daemon.ResolveActiveAccount(ctx, s, p, cc); rErr == nil && found {
		activeID = active.ID
	}

	fetcher := claude.NewUsageFetcher()
	refresher := claude.NewTokenRefresher()

	var refreshed, skipped, failed int
	for _, acct := range accounts {
		if acct.ID == activeID {
			fmt.Fprintf(cmd.OutOrStdout(), "%-18s active — kept fresh by the daemon, skipped\n", acct.Label)
			skipped++
			continue
		}
		lim, err := daemon.RefreshAccountUsage(ctx, s, fetcher, refresher, acct)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%-18s failed: %v\n", acct.Label, err)
			failed++
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-18s 5h %.0f%%  7d %.0f%%\n", acct.Label, lim.FiveHourPct, lim.SevenDayPct)
		refreshed++
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n%d refreshed, %d skipped, %d failed.\n", refreshed, skipped, failed)
	return nil
}
