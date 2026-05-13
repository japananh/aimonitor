-- 0001_init: bootstrap schema for v1.0.0-beta.
--
-- Tables fall into four buckets:
--   - accounts          : pointers to keyring entries (no secrets here)
--   - jsonl_offsets     : per-file resume points for the local-usage watcher
--   - usage_samples     : per-message tokens parsed out of Claude JSONL
--   - probe_results     : cached server-side rate-limit truth (TTL 30s)
--   - switch_audit      : every account switch, manual or automated
--   - settings          : kv store for config that lives in the DB (e.g. autoswitch)

CREATE TABLE accounts (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  provider        TEXT    NOT NULL DEFAULT 'claude',
  label           TEXT    NOT NULL UNIQUE,
  email           TEXT,
  keyring_ref     TEXT    NOT NULL,
  created_at      INTEGER NOT NULL,
  last_used_at    INTEGER
);

CREATE TABLE jsonl_offsets (
  path            TEXT    PRIMARY KEY,
  byte_offset     INTEGER NOT NULL,
  mtime_ns        INTEGER NOT NULL
);

CREATE TABLE usage_samples (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  ts              INTEGER NOT NULL,
  input_tokens    INTEGER NOT NULL,
  output_tokens   INTEGER NOT NULL,
  cache_read      INTEGER NOT NULL DEFAULT 0,
  cache_write     INTEGER NOT NULL DEFAULT 0,
  model           TEXT
);
CREATE INDEX idx_usage_acct_ts ON usage_samples(account_id, ts);

CREATE TABLE probe_results (
  account_id        INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  probed_at         INTEGER NOT NULL,
  tokens_remaining  INTEGER NOT NULL,
  reset_at          INTEGER NOT NULL,
  http_status       INTEGER NOT NULL
);

CREATE TABLE switch_audit (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  ts                    INTEGER NOT NULL,
  from_label            TEXT,
  to_label              TEXT    NOT NULL,
  trigger               TEXT    NOT NULL,
  reason                TEXT,
  from_probed_remaining INTEGER,
  to_probed_remaining   INTEGER
);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
