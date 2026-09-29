-- 0009_account_throttle_strikes: remember recent 429s so the cooldown grows.
--
-- cooldown_until alone forgets everything on the next successful fetch, so a
-- 429 → wait → one success → 429 cycle restarted the backoff at its floor
-- forever (the 2026-09 throttle loop on an exhausted account). The streak
-- survives a success and only resets once 429s stop for a while.
--
-- throttle_strikes: consecutive 429s in the current streak (0 = none).
-- throttled_at: unix millis of the latest 429; NULL when never throttled.

ALTER TABLE accounts ADD COLUMN throttle_strikes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN throttled_at     INTEGER;
