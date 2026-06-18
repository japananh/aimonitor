-- 0006_usage_samples_dedup: turn the dormant usage_samples table (0001)
-- into the backing store for the per-account token breakdown (daily/hourly).
--
-- Claude Code writes the SAME usage-bearing JSONL line several times for a
-- single API response (streaming partials + retries), with identical token
-- counts. Measured on a real transcript: 164 usage lines collapsed to 63
-- unique (message_id, request_id) pairs — summing raw lines over-counts
-- tokens ~2.6x. We add the two identity columns and a UNIQUE index so the
-- daemon can persist with INSERT OR IGNORE and let SQLite drop the dupes.
--
-- Columns are added NOT NULL DEFAULT '' so any pre-existing rows (there are
-- none in practice — nothing ever wrote to this table before 0006) scan
-- cleanly. message.id is globally unique per Anthropic message, so the
-- dedup index is safe to keep global rather than per-account.

ALTER TABLE usage_samples ADD COLUMN message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_samples ADD COLUMN request_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_usage_samples_dedup
  ON usage_samples(message_id, request_id);
