-- 0006_accounts_relogin: flag an account whose OAuth refresh token is dead.
--
-- When a usage refresh hits a 400/401 on the token-refresh endpoint
-- (TokenRefreshExpiredError), the account can't be refreshed or switched to
-- until the user re-logs in via `aimonitor add`. Persist that so the menu-bar
-- popover can show a "Session expired — re-login" badge instead of only
-- surfacing it as a transient error when someone manually hits refresh.
--
-- Cleared on the next successful refresh (or re-add). Distinct from
-- cooldown_until (a 429 is a temporary wait; this needs human action).

ALTER TABLE accounts ADD COLUMN needs_relogin INTEGER NOT NULL DEFAULT 0;
