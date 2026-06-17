package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

func newTokensCmd() *cobra.Command {
	var (
		hourly  bool
		account string
		since   string
	)
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Show token usage per account, bucketed by day or hour",
		Long: `Show how many tokens each account has used, grouped by local-time day
(default) or hour (--hourly).

Counts come from Claude Code's own JSONL transcripts (input, output, cache
read, cache write), deduplicated per API response — not from the OAuth
percentage that ` + "`aimonitor list`" + ` shows. Tokens are attributed to whichever
account was active when each message was written, recorded by the daemon
from the moment it starts watching (no historical backfill).

  aimonitor tokens                      # last 7 days, all accounts, daily
  aimonitor tokens --hourly             # last 24h, all accounts, hourly
  aimonitor tokens --account work       # just the "work" account
  aimonitor tokens --since 30d          # custom window (s/m/h/d/w units)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, _ provider.Provider) error {
				return runTokens(ctx, cmd, s, hourly, account, since)
			})
		},
	}
	cmd.Flags().BoolVar(&hourly, "hourly", false, "bucket by hour instead of by day")
	cmd.Flags().StringVar(&account, "account", "", "limit to one account by label (default: all)")
	cmd.Flags().StringVar(&since, "since", "", "how far back to look, e.g. 24h, 7d, 2w (default: 7d daily, 24h hourly)")
	return cmd
}

func runTokens(ctx context.Context, cmd *cobra.Command, s *store.Store, hourly bool, account, since string) error {
	window, err := parseSince(since, hourly)
	if err != nil {
		return err
	}
	until := time.Now()
	from := until.Add(-window)

	// Resolve the optional account filter and build an id→label map for
	// display. accountID 0 means "all accounts".
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return err
	}
	labelByID := make(map[int64]string, len(accounts))
	for _, a := range accounts {
		labelByID[a.ID] = a.Label
	}

	var accountID int64
	if account != "" {
		acct, gErr := s.GetAccountByLabel(ctx, account)
		if gErr != nil {
			if errors.Is(gErr, store.ErrAccountNotFound) {
				return fmt.Errorf("no account labeled %q (see `aimonitor list`)", account)
			}
			return gErr
		}
		accountID = acct.ID
	}

	var buckets []store.TokenBucket
	if hourly {
		buckets, err = s.TokenUsageByHour(ctx, accountID, from, until)
	} else {
		buckets, err = s.TokenUsageByDay(ctx, accountID, from, until)
	}
	if err != nil {
		return err
	}

	if len(buckets) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(),
			"No token usage recorded in this window. The daemon records usage as you use Claude Code — make sure `aimonitor daemon` is running.")
		return nil
	}

	showAccount := accountID == 0
	// Right-align numeric columns. Every cell (including the last) is
	// tab-terminated so AlignRight pads each into its own column — a
	// non-terminated final cell would abut the previous column.
	out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', tabwriter.AlignRight)
	header := "WHEN\t"
	if showAccount {
		header = "WHEN\tACCOUNT\t"
	}
	fmt.Fprintln(out, header+"INPUT\tOUTPUT\tCACHE R\tCACHE W\tTOTAL\tMSGS\t")

	var tIn, tOut, tCR, tCW, tTot, tMsg int64
	for _, b := range buckets {
		tIn += b.Input
		tOut += b.Output
		tCR += b.CacheRead
		tCW += b.CacheWrite
		tTot += b.Total
		tMsg += b.Messages
		row := b.Bucket + "\t"
		if showAccount {
			row += labelFor(labelByID, b.AccountID) + "\t"
		}
		fmt.Fprintf(out, "%s%s\t%s\t%s\t%s\t%s\t%d\t\n",
			row, comma(b.Input), comma(b.Output), comma(b.CacheRead),
			comma(b.CacheWrite), comma(b.Total), b.Messages)
	}

	// Totals footer. Tab columns line up under the per-bucket numbers.
	sep := "TOTAL\t"
	if showAccount {
		sep = "TOTAL\t\t"
	}
	fmt.Fprintf(out, "%s%s\t%s\t%s\t%s\t%s\t%d\t\n",
		sep, comma(tIn), comma(tOut), comma(tCR), comma(tCW), comma(tTot), tMsg)
	return out.Flush()
}

// labelFor names an account id, falling back to a synthetic "#<id>" when the
// account was removed after its samples were recorded (ON DELETE CASCADE
// removes the rows too, so this is rare, but be defensive).
func labelFor(m map[int64]string, id int64) string {
	if l, ok := m[id]; ok && l != "" {
		return l
	}
	return "#" + strconv.FormatInt(id, 10)
}

// parseSince turns a window string like "24h", "7d", "2w" into a duration.
// Empty defaults to 24h for hourly buckets, 7d for daily. Bare Go durations
// (e.g. "90m") also parse via time.ParseDuration; this adds the day/week
// units Go lacks.
func parseSince(s string, hourly bool) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if hourly {
			return 24 * time.Hour, nil
		}
		return 7 * 24 * time.Hour, nil
	}
	if unit := s[len(s)-1]; unit == 'd' || unit == 'w' {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid --since %q (try 24h, 7d, 2w)", s)
		}
		per := 24 * time.Hour
		if unit == 'w' {
			per = 7 * 24 * time.Hour
		}
		return time.Duration(n) * per, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --since %q (try 24h, 7d, 2w)", s)
	}
	return d, nil
}

// comma formats n with thousands separators (e.g. 1234567 -> "1,234,567").
// Token counts get large; grouping keeps the table readable without pulling
// in a humanize dependency.
func comma(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}
