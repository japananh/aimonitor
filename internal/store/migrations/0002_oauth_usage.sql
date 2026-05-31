-- 0002_oauth_usage: per-account snapshot of the OAuth-introspected
-- rate-limit windows.
--
-- One row per account (PK = account_id). Updates upsert in place. Stored
-- as percentages (REAL) and unix-millis (INTEGER) consistent with the
-- rest of the schema. Source distinguishes "oauth" (api.anthropic.com)
-- from "web" (claude.ai scrape) so the UI can decide which to trust if
-- they disagree.
--
-- Reset columns are nullable: an early build of the OAuth response may
-- omit them, or a future provider may not expose a 7-day window — the
-- UI hides the bar in that case rather than crashing.

CREATE TABLE oauth_usage (
  account_id            INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  five_hour_pct         REAL    NOT NULL DEFAULT 0,
  five_hour_reset_at    INTEGER,
  seven_day_pct         REAL    NOT NULL DEFAULT 0,
  seven_day_reset_at    INTEGER,
  source                TEXT    NOT NULL DEFAULT 'oauth',
  fetched_at            INTEGER NOT NULL
);
