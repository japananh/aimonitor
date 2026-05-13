package cli

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newProbeCmd() *cobra.Command {
	var all bool
	var refresh bool
	cmd := &cobra.Command{
		Use:   "probe [label]",
		Short: "Issue a server-side rate-limit probe and report true remaining tokens",
		Long: `Probe an account's true server-side rate-limit state by issuing one
tiny request against the Anthropic API and parsing the rate-limit
response headers (anthropic-ratelimit-tokens-remaining / -reset).

Probe results are cached for 30 seconds; pass --refresh to skip the cache.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				return runProbe(ctx, cmd, s, p, args, all, refresh)
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "probe every configured account")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ignore the 30s probe cache")
	return cmd
}

func runProbe(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider, args []string, all, refresh bool) error {
	var targets []store.Account
	switch {
	case all && len(args) > 0:
		return errors.New("--all and <label> are mutually exclusive")
	case all:
		var err error
		targets, err = s.ListAccounts(ctx)
		if err != nil {
			return err
		}
	case len(args) == 1:
		acct, err := s.GetAccountByLabel(ctx, args[0])
		if err != nil {
			return err
		}
		targets = []store.Account{acct}
	default:
		return errors.New("specify a <label> or --all")
	}

	if len(targets) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No accounts to probe.")
		return nil
	}

	out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "LABEL\tREMAINING\tRESETS AT\tSOURCE")
	for _, a := range targets {
		rl, src, err := probeWithCache(ctx, s, p, a, refresh)
		if err != nil {
			fmt.Fprintf(out, "%s\tERR\t-\t%s: %v\n", a.Label, src, err)
			continue
		}
		resets := "-"
		if !rl.ResetAt.IsZero() {
			resets = rl.ResetAt.Format("2006-01-02 15:04 MST")
		}
		fmt.Fprintf(out, "%s\t%d\t%s\t%s\n", a.Label, rl.TokensRemaining, resets, src)
	}
	return out.Flush()
}

// probeWithCache returns a RateLimit, its source ('cache' or 'live'), and
// an error from the underlying probe if any. Cache lookups that surface
// ErrProbeStale or ErrProbeNotFound fall through to a live probe.
func probeWithCache(ctx context.Context, s *store.Store, p provider.Provider, a store.Account, refresh bool) (provider.RateLimit, string, error) {
	if !refresh {
		rl, err := s.GetProbeResult(ctx, a.ID)
		if err == nil {
			return rl, fmt.Sprintf("cache (%s ago)", time.Since(rl.ProbedAt).Truncate(time.Second)), nil
		}
		// Fall through on stale / not-found / other errors to a live probe.
	}

	rl, probeErr := p.ProbeServerSide(ctx, provider.Account{
		ID:         a.ID,
		Provider:   a.Provider,
		Label:      a.Label,
		KeyringRef: a.KeyringRef,
	})
	// Even on error, persist whatever we have (e.g. 429 with valid headers).
	if rl.HTTPStatus != 0 || rl.TokensRemaining != 0 {
		_ = s.PutProbeResult(ctx, a.ID, rl)
	}
	if probeErr != nil {
		return rl, "live", probeErr
	}
	return rl, "live", nil
}
