package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

// ErrLimitsNotFound is returned by GetLimits when no row exists yet for
// the requested account. First-fetch case after `aimonitor add`.
var ErrLimitsNotFound = errors.New("store: no oauth_usage row for account")

// GetLimits returns the most-recently-fetched OAuth limits snapshot for
// accountID, or ErrLimitsNotFound when none has been recorded.
//
// No TTL check — staleness is a UI concern (the widget shows fetched_at
// next to the bars so the user can see if data is fresh). The scheduler
// decides when to fetch next; the store just records.
func (s *Store) GetLimits(ctx context.Context, accountID int64) (provider.Limits, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT account_id, five_hour_pct, five_hour_reset_at,
		        seven_day_pct, seven_day_reset_at, source, fetched_at
		   FROM oauth_usage WHERE account_id = ?`, accountID)

	var (
		aid                                   int64
		fivePct, sevenPct                     float64
		fiveResetMs, sevenResetMs             sql.NullInt64
		source                                string
		fetchedAtMs                           int64
	)
	if err := row.Scan(&aid, &fivePct, &fiveResetMs, &sevenPct, &sevenResetMs, &source, &fetchedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return provider.Limits{}, ErrLimitsNotFound
		}
		return provider.Limits{}, fmt.Errorf("read oauth_usage: %w", err)
	}

	l := provider.Limits{
		AccountID:   aid,
		FiveHourPct: fivePct,
		SevenDayPct: sevenPct,
		Source:      source,
		FetchedAt:   time.UnixMilli(fetchedAtMs),
	}
	if fiveResetMs.Valid {
		l.FiveHourResetAt = time.UnixMilli(fiveResetMs.Int64)
	}
	if sevenResetMs.Valid {
		l.SevenDayResetAt = time.UnixMilli(sevenResetMs.Int64)
	}
	return l, nil
}

// PutLimits upserts the snapshot for accountID. FetchedAt is filled with
// time.Now() when the caller leaves it zero.
func (s *Store) PutLimits(ctx context.Context, accountID int64, l provider.Limits) error {
	if accountID == 0 {
		return errors.New("PutLimits: accountID required")
	}
	if l.FetchedAt.IsZero() {
		l.FetchedAt = time.Now()
	}
	if l.Source == "" {
		l.Source = "oauth"
	}

	var fiveReset, sevenReset sql.NullInt64
	if !l.FiveHourResetAt.IsZero() {
		fiveReset = sql.NullInt64{Int64: l.FiveHourResetAt.UnixMilli(), Valid: true}
	}
	if !l.SevenDayResetAt.IsZero() {
		sevenReset = sql.NullInt64{Int64: l.SevenDayResetAt.UnixMilli(), Valid: true}
	}

	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO oauth_usage (account_id, five_hour_pct, five_hour_reset_at,
		                          seven_day_pct, seven_day_reset_at, source, fetched_at)
		      VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET
		      five_hour_pct      = excluded.five_hour_pct,
		      five_hour_reset_at = excluded.five_hour_reset_at,
		      seven_day_pct      = excluded.seven_day_pct,
		      seven_day_reset_at = excluded.seven_day_reset_at,
		      source             = excluded.source,
		      fetched_at         = excluded.fetched_at`,
		accountID, l.FiveHourPct, fiveReset, l.SevenDayPct, sevenReset, l.Source, l.FetchedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("write oauth_usage: %w", err)
	}
	return nil
}
