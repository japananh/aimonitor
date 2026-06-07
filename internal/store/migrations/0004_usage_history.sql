-- 0004_usage_history: time series of OAuth-introspected utilization per
-- account, powering the sparkline trend in the widget.
--
-- DISTINCT from the legacy usage_samples table (0001), which records
-- per-message token counts parsed out of Claude JSONL. This table records
-- the 5h/7d *percentage* snapshots the UsageScheduler already fetches —
-- one row appended per successful fetch (active, inactive round-robin, or
-- candidate refresh), pruned to a rolling window so it can't grow without
-- bound. Stored as REAL percentages + unix-millis, consistent with
-- oauth_usage.

CREATE TABLE usage_history (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  ts            INTEGER NOT NULL,   -- unix millis of the fetch
  five_hour_pct REAL    NOT NULL DEFAULT 0,
  seven_day_pct REAL    NOT NULL DEFAULT 0
);
CREATE INDEX idx_usage_history_acct_ts ON usage_history(account_id, ts);
