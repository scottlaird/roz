-- The action an exception raised, and the condition it was raised for.
--
-- Exceptions are durable and queryable, so nothing is lost when one fires. But
-- an exception never reaches the queue: acting on one depends on catching the
-- monitor line as it goes past, or on thinking to run the query, and neither
-- is something to rely on. A wait that went bad on Friday is discoverable on
-- Monday only by looking for it. An action would simply be there.
--
-- This table is what makes "one action per outstanding condition" answerable.
-- Without it the only way to ask whether a condition already has something in
-- the queue is to guess from titles, and an exception rule that re-reports
-- would multiply items instead of staying quiet.
--
-- A condition is (kind, subject): this exception, about this thing. Not the
-- firing — the same condition firing twice is still one thing to do, and the
-- index below is what the check reads.
--
-- The subject pair is the informal-FK pattern `event` and `priority_target`
-- already use: 'action' -> 'NA57', 'pr' -> 'owner/repo#1'. Not enforced,
-- because the referent is polymorphic.
--
-- Keyed on action_id, so an action is raised by at most one condition. An
-- action that would answer two is one somebody should have split.
CREATE TABLE raised_action (
  action_id    TEXT PRIMARY KEY REFERENCES action(id),
  kind         TEXT NOT NULL,   -- the exception kind, e.g. 'waited_too_long'
  subject_type TEXT NOT NULL,
  subject_id   TEXT NOT NULL,
  created_at   TEXT NOT NULL
) STRICT;

-- The lookup the de-duplication does, every time an exception considers
-- raising something.
CREATE INDEX raised_action_condition
  ON raised_action(kind, subject_type, subject_id);
