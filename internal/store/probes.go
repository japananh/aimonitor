package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/japananh/aimonitor/internal/provider"
)

// ProbeCacheTTL is how long a cached probe result is considered fresh.
// Tuning constraint: too short and we burn extra quota on probes; too
// long and auto-switch reacts to stale truth. 30s is a balance.
const ProbeCacheTTL = 30 * time.Second

// GetProbeResult returns the cached probe for accountID if it is younger
// than ProbeCacheTTL. Returns ErrProbeNotFound (or stale) otherwise.
func (s *Store) GetProbeResult(ctx context.Context, accountID int64) (provider.RateLimit, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT account_id, probed_at, tokens_remaining, reset_at, http_status
		 FROM probe_results WHERE account_id = ?`, accountID)

	var aid, probedAtMs, tokensRemaining, resetAtMs int64
	var httpStatus int
	if err := row.Scan(&aid, &probedAtMs, &tokensRemaining, &resetAtMs, &httpStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return provider.RateLimit{}, ErrProbeNotFound
		}
		return provider.RateLimit{}, fmt.Errorf("read probe: %w", err)
	}

	rl := provider.RateLimit{
		AccountID:       aid,
		ProbedAt:        time.UnixMilli(probedAtMs),
		TokensRemaining: tokensRemaining,
		ResetAt:         time.UnixMilli(resetAtMs),
		HTTPStatus:      httpStatus,
	}
	if time.Since(rl.ProbedAt) > ProbeCacheTTL {
		return rl, ErrProbeStale
	}
	return rl, nil
}

// PutProbeResult upserts (one row per account).
func (s *Store) PutProbeResult(ctx context.Context, accountID int64, rl provider.RateLimit) error {
	if accountID == 0 {
		return errors.New("PutProbeResult: accountID required")
	}
	if rl.ProbedAt.IsZero() {
		rl.ProbedAt = time.Now()
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO probe_results (account_id, probed_at, tokens_remaining, reset_at, http_status)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET
		   probed_at = excluded.probed_at,
		   tokens_remaining = excluded.tokens_remaining,
		   reset_at = excluded.reset_at,
		   http_status = excluded.http_status`,
		accountID, rl.ProbedAt.UnixMilli(), rl.TokensRemaining, rl.ResetAt.UnixMilli(), rl.HTTPStatus,
	)
	if err != nil {
		return fmt.Errorf("write probe: %w", err)
	}
	return nil
}

// ErrProbeNotFound is returned by GetProbeResult when no row exists yet
// for the requested account.
var ErrProbeNotFound = errors.New("store: no probe result for account")

// ErrProbeStale is returned by GetProbeResult when the cached row is
// older than ProbeCacheTTL. The returned RateLimit is still populated —
// callers can choose to use it as a hint while the auto-switcher
// reissues a fresh probe.
var ErrProbeStale = errors.New("store: probe result is stale")
