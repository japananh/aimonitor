-- 0007_usage_samples_project: record which Claude Code project each token
-- sample came from, so usage can be broken down per project (not just per
-- account / day / model).
--
-- The daemon derives the project from the JSONL file's parent directory name
-- under ~/.claude/projects (Claude Code encodes the working directory there,
-- e.g. "-Users-nana-workspace-japananh"). Stored raw; the CLI prettifies it
-- back to a path for display. NOT NULL DEFAULT '' so the column is part of no
-- index and pre-0007 rows (recorded before this column existed) read as ''.

ALTER TABLE usage_samples ADD COLUMN project TEXT NOT NULL DEFAULT '';
