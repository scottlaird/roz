-- When a pull request closed, and how far back the poll reaches.
--
-- closed_at is the anchor a grace window needs. merged_at answers it for a
-- merged pull request and says nothing about a closed one, and keying the
-- window off merged_at alone would leave every CLOSED row either permanently
-- inside the window or permanently outside it, depending on how the NULL
-- fell. GitHub reports closedAt for both, so this is one column that dates
-- either ending.
--
-- Observed, like every other thing GitHub knows. NULL means still open -- or,
-- for a row recorded before this column existed, that nobody has read it
-- since. That second case is why an undated terminal pull request stays in
-- the poll set: one more read gives it a date, and the next window drops it.
ALTER TABLE pr ADD COLUMN closed_at TEXT;

-- How many days after a pull request ends to keep asking about it.
--
-- Days rather than hours because the thing it covers is slow: review comments
-- and thread resolutions land after a merge, and human_commented_at is read
-- by the amend-versus-new-commit rule. Generous rather than tight -- the cost
-- of a wide window is a few entities per poll, and the cost of a narrow one
-- is an observation nobody makes.
ALTER TABLE config ADD COLUMN poll_window_days INTEGER NOT NULL DEFAULT 14
  CHECK (poll_window_days >= 0);

-- The poll set is open pull requests plus recently-ended ones, so this is
-- what it is read against.
CREATE INDEX pr_poll_window ON pr(closed_at) WHERE closed_at IS NOT NULL;
