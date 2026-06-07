-- 0005_account_cooldown: per-account rate-limit cooldown.
--
-- When Anthropic 429s a usage fetch for an account, the daemon parks that
-- account until cooldown_until (unix millis) — honoring the server's
-- Retry-After when present. While parked, the account is skipped by the
-- background inactive poller AND excluded from the auto-swap candidate
-- pool, so we don't keep hammering (or switch INTO) an account that's
-- actively throttled by the shared user base. Cleared on the next
-- successful fetch. reason is a short human label for the widget badge.
--
-- Both nullable: NULL cooldown_until == not cooling. Reads COALESCE to 0/''.

ALTER TABLE accounts ADD COLUMN cooldown_until  INTEGER;
ALTER TABLE accounts ADD COLUMN cooldown_reason TEXT;
