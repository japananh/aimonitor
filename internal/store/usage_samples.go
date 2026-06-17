package store

import (
	"context"
	"fmt"
	"time"
)

// UsageSamplesRetention is how far back per-message token rows are kept.
// Daily/hourly breakdowns rarely look past a few weeks; 90 days is a
// generous ceiling that keeps the table bounded. Pruned opportunistically
// (see PruneUsageSamples), not on every insert — inserts arrive in bursts
// of hundreds during an active session and shouldn't each pay a DELETE.
const UsageSamplesRetention = 90 * 24 * time.Hour

// TokenSample is one per-message token data point destined for the
// usage_samples table. It mirrors the fields the Claude JSONL parser
// extracts (provider/claude.Sample); the daemon maps one to the other so
// the store package stays free of provider-specific types, consistent with
// how UsageSample (usage_history) is kept provider-agnostic.
//
// MessageID + RequestID are the dedup key: Claude Code writes a usage line
// several times per response (streaming/retries), so InsertUsageSample
// relies on the UNIQUE(message_id, request_id) index (migration 0006) to
// keep exactly one row.
type TokenSample struct {
	Ts         time.Time
	MessageID  string
	RequestID  string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Model      string
	// Project is the Claude Code project the sample came from — the raw
	// encoded directory name under ~/.claude/projects (the daemon derives it
	// from the JSONL path). Empty when unknown.
	Project string
}

// InsertUsageSample records one per-message token sample for accountID,
// deduplicating on (message_id, request_id). Claude Code logs a usage line
// several times for one API response, and crucially the counts GROW across
// those lines (streaming partials: output_tokens climbs as the response
// completes — measured at ~8% of messages in real transcripts). So we can't
// just keep the first line (that undercounts); we upsert the per-field MAX,
// which converges on the final/complete counts regardless of the order the
// watcher sees the partials in. The WHERE guard makes an identical or
// smaller duplicate a true no-op. Ts defaults to now when zero.
//
// Returns true when the write changed the table (a new row, or a partial
// that raised an existing row's counts), false when it was a no-op
// duplicate — used only to pace opportunistic pruning, so precision here
// isn't load-bearing.
func (s *Store) InsertUsageSample(ctx context.Context, accountID int64, t TokenSample) (bool, error) {
	if accountID == 0 {
		return false, fmt.Errorf("InsertUsageSample: accountID required")
	}
	ts := t.Ts
	if ts.IsZero() {
		ts = time.Now()
	}
	// project is set only on insert; it's stable for a message, so the
	// conflict path (streaming partials) leaves it untouched.
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO usage_samples
		   (account_id, ts, input_tokens, output_tokens, cache_read, cache_write, model, message_id, request_id, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(message_id, request_id) DO UPDATE SET
		   input_tokens  = MAX(usage_samples.input_tokens,  excluded.input_tokens),
		   output_tokens = MAX(usage_samples.output_tokens, excluded.output_tokens),
		   cache_read    = MAX(usage_samples.cache_read,    excluded.cache_read),
		   cache_write   = MAX(usage_samples.cache_write,   excluded.cache_write)
		 WHERE excluded.input_tokens  > usage_samples.input_tokens
		    OR excluded.output_tokens > usage_samples.output_tokens
		    OR excluded.cache_read    > usage_samples.cache_read
		    OR excluded.cache_write   > usage_samples.cache_write`,
		accountID, ts.UnixMilli(), t.Input, t.Output, t.CacheRead, t.CacheWrite, t.Model, t.MessageID, t.RequestID, t.Project,
	)
	if err != nil {
		return false, fmt.Errorf("insert usage_samples: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, nil // write succeeded; can't tell changed-vs-noop — best-effort
	}
	return n > 0, nil
}

// PruneUsageSamples deletes rows older than retention and returns the count
// removed. Global (not per-account): message_id is globally unique, so the
// dedup index — and this prune — are not scoped by account. Call
// occasionally (e.g. once per daemon-poll cycle), not per insert.
func (s *Store) PruneUsageSamples(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention).UnixMilli()
	res, err := s.DB.ExecContext(ctx,
		`DELETE FROM usage_samples WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune usage_samples: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
