-- config_audit records every change made through `aimonitor config set`
-- (and config import), so a setting that flips — e.g. mcp.slack.enabled going
-- to "false" — can be traced back to when it happened and from what source.
-- The daemon's own setting writes (daemon_status, etc.) don't go through these
-- paths, so this table stays low-volume and user-meaningful.
CREATE TABLE IF NOT EXISTS config_audit (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    ts        INTEGER NOT NULL,        -- unix millis
    key       TEXT    NOT NULL,
    old_value TEXT,                    -- NULL when the key was previously unset
    new_value TEXT    NOT NULL,
    source    TEXT    NOT NULL DEFAULT 'cli'
);

CREATE INDEX IF NOT EXISTS idx_config_audit_key_ts ON config_audit(key, ts);
