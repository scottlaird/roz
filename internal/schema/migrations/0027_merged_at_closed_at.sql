-- When a pull request merged, and when an issue closed.
--
-- Both are observed, and both come from the third party that owns the fact:
-- GitHub's own mergedAt and closedAt. They are stored rather than derived from
-- the log because the log records when roz *noticed*, which is a different
-- thing and wrong in the direction that matters. A pull request tracked after
-- the fact, or one that merged while roz was not running, has a state
-- transition dated to the poll that caught up — days late, or absent entirely
-- for one that was already MERGED the first time it was read.
--
-- What they are for is the week in review: "what did I merge", "what closed",
-- ordered by when it happened. Neither question can be asked of state alone,
-- which says only where something is now.
--
-- NULL means not merged, not closed, or — for a Jira issue — not something
-- anybody has said. Nothing reads Jira, so closed_at there is only ever what
-- `roz issue observe --closed-at` writes, and its absence is not evidence the
-- issue is open. state is still what says that.
ALTER TABLE pr ADD COLUMN merged_at TEXT;
ALTER TABLE tracker_issue ADD COLUMN closed_at TEXT;

-- Both listings filter on a window and order by it, over what is normally the
-- small tail of a much larger table.
CREATE INDEX pr_merged_at ON pr(merged_at) WHERE merged_at IS NOT NULL;
CREATE INDEX tracker_issue_closed_at ON tracker_issue(closed_at) WHERE closed_at IS NOT NULL;
