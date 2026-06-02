-- 0003_account_identity: capture the Claude account identity (org) so a
-- switch can patch ~/.claude.json's oauthAccount to match the swapped
-- tokens.
--
-- The keychain credential blob holds only tokens, never identity —
-- email/org live in ~/.claude.json. Storing them here lets `aimonitor
-- switch` rewrite oauthAccount to the target account, keeping Claude
-- Code's notion of "who am I" consistent with the live tokens.
--
-- `email` already exists (0001). Identity for dedup is (email,
-- organization_uuid): the same address in two orgs is two distinct
-- accounts. Both default to '' so existing rows upgrade cleanly; the
-- next `aimonitor add`/`switch` backfills them from ~/.claude.json.

ALTER TABLE accounts ADD COLUMN organization_uuid TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN organization_name TEXT NOT NULL DEFAULT '';
