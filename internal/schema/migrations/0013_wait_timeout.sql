-- A wait that has gone on too long should say so.
--
-- Excluding rank_class = 'wait' from --unblocked was right: four of ten queue
-- items were waits, and a queue full of things you cannot act on is not a
-- queue. But it left nothing speaking up when a wait went bad. The page has a
-- waiting block, so they are visible to anyone who goes and looks — a page you
-- have to remember to read is one step away from no signal at all.
--
-- This is not a snooze, and the distinction is the whole design: a snooze
-- hides something until a date, this reveals something after one.

-- The budget, per verb. NULL means this verb never times out, which is the
-- right default for the ones that are your own work rather than somebody
-- else's — nothing is waiting, so nothing can be overdue.
--
-- On the verb because that is where "how long is reasonable" belongs: it is a
-- property of the kind of waiting, not of the item. Data rather than code, so
-- changing an allowance is a row.
ALTER TABLE actionverb ADD COLUMN wait_days INTEGER;

-- The per-action override, for the one that is different. NULL is the
-- ordinary case and means the verb's budget, resolved when the deadline is
-- checked rather than copied here — so changing an allowance reaches the
-- actions that never claimed an exception to it. Same shape as pr.pipeline
-- against its repository.
ALTER TABLE action ADD COLUMN okay_to_wait_until TEXT;

-- Waiting on a review is the case this was built for, and a merge that has
-- been one click away for a day is the other. Everything else is left NULL:
-- a verb that describes your own work cannot be overdue, only undone.
UPDATE actionverb SET wait_days = 3 WHERE verb = 'wait_review';
UPDATE actionverb SET wait_days = 1 WHERE verb = 'merge';
