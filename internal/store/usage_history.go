package store

import (
	"context"
	"fmt"
	"time"
)

// UsageHistoryRetention is how far back usage_history rows are kept. A touch
// over the 7-day window so a 7-day sparkline always has full coverage; older
// points are pruned on every append (cheap, index-backed, per-account).
const UsageHistoryRetention = 8 * 24 * time.Hour

// UsageSample is one point in an account's utilization time series.
type UsageSample struct {
	Ts          time.Time
	FiveHourPct float64
	SevenDayPct float64
}

// AppendUsageHistory records one utilization point for accountID and prunes
// that account's points older than UsageHistoryRetention. ts is taken from
// l.FetchedAt, falling back to now when zero.
//
// Best-effort by contract: callers (and PutLimits) treat a history failure as
// non-fatal — a dropped trend point is invisible, never a reason to fail the
// authoritative oauth_usage write.
func (s *Store) AppendUsageHistory(ctx context.Context, accountID int64, sample UsageSample) error {
	if accountID == 0 {
		return fmt.Errorf("AppendUsageHistory: accountID required")
	}
	ts := sample.Ts
	if ts.IsZero() {
		ts = time.Now()
	}
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO usage_history (account_id, ts, five_hour_pct, seven_day_pct)
		      VALUES (?, ?, ?, ?)`,
		accountID, ts.UnixMilli(), sample.FiveHourPct, sample.SevenDayPct,
	); err != nil {
		return fmt.Errorf("insert usage_history: %w", err)
	}
	// Prune this account's old points inline. Scoped by account_id so the
	// (account_id, ts) index drives the delete; bounded work per append.
	cutoff := ts.Add(-UsageHistoryRetention).UnixMilli()
	if _, err := s.DB.ExecContext(ctx,
		`DELETE FROM usage_history WHERE account_id = ? AND ts < ?`,
		accountID, cutoff,
	); err != nil {
		return fmt.Errorf("prune usage_history: %w", err)
	}
	return nil
}

// ListUsageHistory returns accountID's utilization points at or after since,
// ordered oldest-first. An account with no points yields an empty slice.
func (s *Store) ListUsageHistory(ctx context.Context, accountID int64, since time.Time) ([]UsageSample, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT ts, five_hour_pct, seven_day_pct
		   FROM usage_history
		  WHERE account_id = ? AND ts >= ?
		  ORDER BY ts ASC`,
		accountID, since.UnixMilli(),
	)
	if err != nil {
		return nil, fmt.Errorf("list usage_history: %w", err)
	}
	defer rows.Close()

	var out []UsageSample
	for rows.Next() {
		var tsMs int64
		var five, seven float64
		if err := rows.Scan(&tsMs, &five, &seven); err != nil {
			return nil, err
		}
		out = append(out, UsageSample{
			Ts:          time.UnixMilli(tsMs),
			FiveHourPct: five,
			SevenDayPct: seven,
		})
	}
	return out, rows.Err()
}
