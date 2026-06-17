package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
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
		hourly    bool
		byModel   bool
		byProject bool
		account   string
		since     string
		format    string
	)
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Show token usage per account, bucketed by day, hour, model, or project",
		Long: `Show how many tokens each account has used, grouped by local-time day
(default), hour (--hourly), model (--by-model), or project (--by-project).

Counts come from Claude Code's own JSONL transcripts (input, output, cache
read, cache write), deduplicated per API response — not from the OAuth
percentage that ` + "`aimonitor list`" + ` shows. Tokens are attributed to whichever
account was active when each message was written, recorded by the daemon
from the moment it starts watching (no historical backfill).

  aimonitor tokens                      # last 7 days, all accounts, daily
  aimonitor tokens --hourly             # last 24h, all accounts, hourly
  aimonitor tokens --by-model           # per-model totals over the window
  aimonitor tokens --by-project         # per-project totals over the window
  aimonitor tokens --account work       # just the "work" account
  aimonitor tokens --since 30d          # custom window (s/m/h/d/w units)
  aimonitor tokens --format csv > t.csv # export (csv or json), any grouping`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, _ provider.Provider) error {
				return runTokens(ctx, cmd, s, tokensOpts{
					hourly: hourly, byModel: byModel, byProject: byProject,
					account: account, since: since, format: format,
				})
			})
		},
	}
	cmd.Flags().BoolVar(&hourly, "hourly", false, "bucket by hour instead of by day")
	cmd.Flags().BoolVar(&byModel, "by-model", false, "summarize per model over the window instead of by time")
	cmd.Flags().BoolVar(&byProject, "by-project", false, "summarize per project over the window instead of by time")
	cmd.Flags().StringVar(&account, "account", "", "limit to one account by label (default: all)")
	cmd.Flags().StringVar(&since, "since", "", "how far back to look, e.g. 24h, 7d, 2w (default: 7d, or 24h with --hourly)")
	cmd.Flags().StringVar(&format, "format", "table", "output format: table, csv, or json")
	return cmd
}

type tokensOpts struct {
	hourly    bool
	byModel   bool
	byProject bool
	account   string
	since     string
	format    string
}

// tokenRow is the format-agnostic shape every grouping (day/hour/model/
// project) is reduced to before rendering, so table/csv/json share one path.
type tokenRow struct {
	key        string // the dimension value: a date/hour bucket, a model, or a project
	accountID  int64
	input      int64
	output     int64
	cacheRead  int64
	cacheWrite int64
	total      int64
	messages   int64
}

func runTokens(ctx context.Context, cmd *cobra.Command, s *store.Store, o tokensOpts) error {
	if o.byModel && o.byProject {
		return errors.New("--by-model and --by-project are mutually exclusive")
	}
	switch o.format {
	case "table", "csv", "json":
	default:
		return fmt.Errorf("invalid --format %q (want table, csv, or json)", o.format)
	}

	window, err := parseSince(o.since, o.hourly)
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
	if o.account != "" {
		acct, gErr := s.GetAccountByLabel(ctx, o.account)
		if gErr != nil {
			if errors.Is(gErr, store.ErrAccountNotFound) {
				return fmt.Errorf("no account labeled %q (see `aimonitor list`)", o.account)
			}
			return gErr
		}
		accountID = acct.ID
	}

	dimension, rows, err := fetchTokenRows(ctx, s, accountID, from, until, o)
	if err != nil {
		return err
	}

	switch o.format {
	case "csv":
		return renderTokensCSV(cmd, dimension, rows, labelByID)
	case "json":
		return renderTokensJSON(cmd, dimension, rows, labelByID)
	default:
		return renderTokensTable(cmd, dimension, rows, accountID == 0, labelByID)
	}
}

// fetchTokenRows runs the query for the chosen grouping and flattens it into
// tokenRows, returning the dimension name ("day"/"hour"/"model"/"project").
func fetchTokenRows(ctx context.Context, s *store.Store, accountID int64, from, until time.Time, o tokensOpts) (string, []tokenRow, error) {
	switch {
	case o.byModel:
		ms, err := s.TokenUsageByModel(ctx, accountID, from, until)
		if err != nil {
			return "", nil, err
		}
		rows := make([]tokenRow, 0, len(ms))
		for _, m := range ms {
			rows = append(rows, tokenRow{m.Model, m.AccountID, m.Input, m.Output, m.CacheRead, m.CacheWrite, m.Total, m.Messages})
		}
		return "model", rows, nil
	case o.byProject:
		ps, err := s.TokenUsageByProject(ctx, accountID, from, until)
		if err != nil {
			return "", nil, err
		}
		rows := make([]tokenRow, 0, len(ps))
		for _, p := range ps {
			rows = append(rows, tokenRow{prettyProject(p.Project), p.AccountID, p.Input, p.Output, p.CacheRead, p.CacheWrite, p.Total, p.Messages})
		}
		return "project", rows, nil
	default:
		var bs []store.TokenBucket
		var err error
		if o.hourly {
			bs, err = s.TokenUsageByHour(ctx, accountID, from, until)
		} else {
			bs, err = s.TokenUsageByDay(ctx, accountID, from, until)
		}
		if err != nil {
			return "", nil, err
		}
		rows := make([]tokenRow, 0, len(bs))
		for _, b := range bs {
			rows = append(rows, tokenRow{b.Bucket, b.AccountID, b.Input, b.Output, b.CacheRead, b.CacheWrite, b.Total, b.Messages})
		}
		dim := "day"
		if o.hourly {
			dim = "hour"
		}
		return dim, rows, nil
	}
}

// keyHeader is the table/CSV column title for a dimension.
func keyHeader(dimension string) string {
	switch dimension {
	case "model":
		return "MODEL"
	case "project":
		return "PROJECT"
	default:
		return "WHEN" // day or hour
	}
}

func renderTokensTable(cmd *cobra.Command, dimension string, rows []tokenRow, showAccount bool, labelByID map[int64]string) error {
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(),
			"No token usage recorded in this window. The daemon records usage as you use Claude Code — make sure `aimonitor daemon` is running.")
		return nil
	}
	// Right-align numeric columns. Every cell (including the last) is
	// tab-terminated so AlignRight pads each into its own column — a
	// non-terminated final cell would abut the previous column.
	out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', tabwriter.AlignRight)
	head := keyHeader(dimension) + "\t"
	if showAccount {
		head += "ACCOUNT\t"
	}
	fmt.Fprintln(out, head+"INPUT\tOUTPUT\tCACHE R\tCACHE W\tTOTAL\tMSGS\t")

	var tIn, tOut, tCR, tCW, tTot, tMsg int64
	for _, r := range rows {
		tIn += r.input
		tOut += r.output
		tCR += r.cacheRead
		tCW += r.cacheWrite
		tTot += r.total
		tMsg += r.messages
		line := r.key + "\t"
		if showAccount {
			line += labelFor(labelByID, r.accountID) + "\t"
		}
		fmt.Fprintf(out, "%s%s\t%s\t%s\t%s\t%s\t%d\t\n",
			line, comma(r.input), comma(r.output), comma(r.cacheRead),
			comma(r.cacheWrite), comma(r.total), r.messages)
	}
	sep := "TOTAL\t"
	if showAccount {
		sep = "TOTAL\t\t"
	}
	fmt.Fprintf(out, "%s%s\t%s\t%s\t%s\t%s\t%d\t\n",
		sep, comma(tIn), comma(tOut), comma(tCR), comma(tCW), comma(tTot), tMsg)
	return out.Flush()
}

func renderTokensCSV(cmd *cobra.Command, dimension string, rows []tokenRow, labelByID map[int64]string) error {
	w := csv.NewWriter(cmd.OutOrStdout())
	// The dimension is the first column; account is always present for
	// machine consumers (unlike the table, which hides it when filtered).
	if err := w.Write([]string{dimension, "account", "input", "output", "cache_read", "cache_write", "total", "messages"}); err != nil {
		return err
	}
	for _, r := range rows {
		rec := []string{
			r.key, labelFor(labelByID, r.accountID),
			strconv.FormatInt(r.input, 10), strconv.FormatInt(r.output, 10),
			strconv.FormatInt(r.cacheRead, 10), strconv.FormatInt(r.cacheWrite, 10),
			strconv.FormatInt(r.total, 10), strconv.FormatInt(r.messages, 10),
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func renderTokensJSON(cmd *cobra.Command, dimension string, rows []tokenRow, labelByID map[int64]string) error {
	type rec struct {
		Key        string `json:"key"`
		Dimension  string `json:"dimension"`
		Account    string `json:"account"`
		Input      int64  `json:"input"`
		Output     int64  `json:"output"`
		CacheRead  int64  `json:"cache_read"`
		CacheWrite int64  `json:"cache_write"`
		Total      int64  `json:"total"`
		Messages   int64  `json:"messages"`
	}
	recs := make([]rec, 0, len(rows))
	for _, r := range rows {
		recs = append(recs, rec{
			Key: r.key, Dimension: dimension, Account: labelFor(labelByID, r.accountID),
			Input: r.input, Output: r.output, CacheRead: r.cacheRead,
			CacheWrite: r.cacheWrite, Total: r.total, Messages: r.messages,
		})
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(recs)
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

// prettyProject turns the raw encoded project dir name Claude Code uses
// (e.g. "-Users-nana-workspace-japananh") back into an approximate path
// ("/Users/nana/workspace/japananh") for display. Lossy for original paths
// containing literal hyphens, but readable. "(unknown)" and "" pass through.
func prettyProject(dir string) string {
	if dir == "" || dir == "(unknown)" {
		return "(unknown)"
	}
	return "/" + strings.ReplaceAll(strings.TrimPrefix(dir, "-"), "-", "/")
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
