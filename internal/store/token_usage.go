package store

import (
	"context"
	"fmt"
	"time"
)

// TokenBucket is one time bucket (a local-time day or hour) of aggregated
// token usage for a single account. Total is the sum of all four token
// kinds — input + output + cache read + cache write — matching how Claude
// Code itself reports "tokens used".
type TokenBucket struct {
	// Bucket is a local-time label: "2006-01-02" for daily,
	// "2006-01-02 15:00" for hourly. Buckets are computed in the machine's
	// local timezone (see the strftime 'localtime' modifier) so "today" and
	// "this hour" line up with the user's wall clock, not UTC.
	Bucket     string
	AccountID  int64
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Total      int64
	Messages   int64
}

// TokenUsageByDay returns per-day token totals in [since, until), bucketed
// by local calendar day. When accountID is 0, every account is included
// and rows are grouped per (day, account); otherwise only that account.
// Rows are ordered oldest day first, then account_id.
func (s *Store) TokenUsageByDay(ctx context.Context, accountID int64, since, until time.Time) ([]TokenBucket, error) {
	return s.tokenUsage(ctx, "%Y-%m-%d", accountID, since, until)
}

// TokenUsageByHour is TokenUsageByDay at hour granularity; buckets are
// "YYYY-MM-DD HH:00" in local time.
func (s *Store) TokenUsageByHour(ctx context.Context, accountID int64, since, until time.Time) ([]TokenBucket, error) {
	return s.tokenUsage(ctx, "%Y-%m-%d %H:00", accountID, since, until)
}

// ModelUsage is per-(account, model) token totals over a window, with no
// time bucketing — powers `aimonitor tokens --by-model` ("which model burned
// the tokens"). Total sums all four token kinds, same as TokenBucket.
type ModelUsage struct {
	AccountID  int64
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Total      int64
	Messages   int64
}

// TokenUsageByModel returns per-model token totals in [since, until). When
// accountID is 0 every account is included and rows are grouped per
// (account, model); otherwise only that account. Rows are ordered by account
// then descending total (biggest spender first). An empty/NULL model is
// reported as "(unknown)".
func (s *Store) TokenUsageByModel(ctx context.Context, accountID int64, since, until time.Time) ([]ModelUsage, error) {
	args := []any{since.UnixMilli(), until.UnixMilli()}
	acctFilter := ""
	if accountID != 0 {
		acctFilter = "AND account_id = ?"
		args = append(args, accountID)
	}
	query := fmt.Sprintf(`
		SELECT account_id,
		       COALESCE(NULLIF(model, ''), '(unknown)') AS model,
		       SUM(input_tokens)  AS input,
		       SUM(output_tokens) AS output,
		       SUM(cache_read)    AS cache_read,
		       SUM(cache_write)   AS cache_write,
		       SUM(input_tokens + output_tokens + cache_read + cache_write) AS total,
		       COUNT(*) AS messages
		  FROM usage_samples
		 WHERE ts >= ? AND ts < ? %s
		 GROUP BY account_id, model
		 ORDER BY account_id ASC, total DESC`, acctFilter)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("token usage by model: %w", err)
	}
	defer rows.Close()

	var out []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.AccountID, &m.Model, &m.Input, &m.Output,
			&m.CacheRead, &m.CacheWrite, &m.Total, &m.Messages); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ProjectUsage is per-(account, project) token totals over a window — powers
// `aimonitor tokens --by-project`. Project is the raw encoded directory name
// under ~/.claude/projects; callers prettify it for display.
type ProjectUsage struct {
	AccountID  int64
	Project    string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Total      int64
	Messages   int64
}

// TokenUsageByProject returns per-project token totals in [since, until),
// grouped per (account, project), ordered by account then descending total.
// An empty project is reported as "(unknown)".
func (s *Store) TokenUsageByProject(ctx context.Context, accountID int64, since, until time.Time) ([]ProjectUsage, error) {
	args := []any{since.UnixMilli(), until.UnixMilli()}
	acctFilter := ""
	if accountID != 0 {
		acctFilter = "AND account_id = ?"
		args = append(args, accountID)
	}
	query := fmt.Sprintf(`
		SELECT account_id,
		       COALESCE(NULLIF(project, ''), '(unknown)') AS project,
		       SUM(input_tokens)  AS input,
		       SUM(output_tokens) AS output,
		       SUM(cache_read)    AS cache_read,
		       SUM(cache_write)   AS cache_write,
		       SUM(input_tokens + output_tokens + cache_read + cache_write) AS total,
		       COUNT(*) AS messages
		  FROM usage_samples
		 WHERE ts >= ? AND ts < ? %s
		 GROUP BY account_id, project
		 ORDER BY account_id ASC, total DESC`, acctFilter)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("token usage by project: %w", err)
	}
	defer rows.Close()

	var out []ProjectUsage
	for rows.Next() {
		var p ProjectUsage
		if err := rows.Scan(&p.AccountID, &p.Project, &p.Input, &p.Output,
			&p.CacheRead, &p.CacheWrite, &p.Total, &p.Messages); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// tokenUsage is the shared aggregation. format is the strftime pattern that
// defines the bucket granularity; ts is stored as unix millis so we divide
// by 1000 for strftime's unixepoch input and apply 'localtime' so day/hour
// boundaries follow the local timezone.
func (s *Store) tokenUsage(ctx context.Context, format string, accountID int64, since, until time.Time) ([]TokenBucket, error) {
	args := []any{format, since.UnixMilli(), until.UnixMilli()}
	acctFilter := ""
	if accountID != 0 {
		acctFilter = "AND account_id = ?"
		args = append(args, accountID)
	}

	// strftime(format, ts/1000, 'unixepoch', 'localtime') buckets in local
	// time. The token kinds are summed independently plus a grand Total;
	// COUNT(*) is the number of (already-deduped) messages in the bucket.
	query := fmt.Sprintf(`
		SELECT strftime(?, ts/1000, 'unixepoch', 'localtime') AS bucket,
		       account_id,
		       SUM(input_tokens)  AS input,
		       SUM(output_tokens) AS output,
		       SUM(cache_read)    AS cache_read,
		       SUM(cache_write)   AS cache_write,
		       SUM(input_tokens + output_tokens + cache_read + cache_write) AS total,
		       COUNT(*) AS messages
		  FROM usage_samples
		 WHERE ts >= ? AND ts < ? %s
		 GROUP BY bucket, account_id
		 ORDER BY bucket ASC, account_id ASC`, acctFilter)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("token usage aggregate: %w", err)
	}
	defer rows.Close()

	var out []TokenBucket
	for rows.Next() {
		var b TokenBucket
		if err := rows.Scan(&b.Bucket, &b.AccountID, &b.Input, &b.Output,
			&b.CacheRead, &b.CacheWrite, &b.Total, &b.Messages); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
