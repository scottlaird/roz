-- Waiting on work that is not ours.
--
-- wait_ref covers "wait for v1.2.4 to be cut" and wait_review covers "wait for
-- somebody to approve my pull request". There was no way to say "this is
-- blocked on an issue in somebody else's repository closing" -- which today is
-- a plain wait nobody can close except by hand, sitting in the queue looking
-- live until a person happens to notice the issue closed.
--
-- 0032's predecessor made the fact readable: tracker_issue.closed_at is polled
-- on every sync and nothing consumed it. This is what consumes it.
ALTER TABLE actionverb ADD COLUMN requires_issue INTEGER NOT NULL DEFAULT 0
  CHECK (requires_issue IN (0,1));

INSERT INTO actionverb
  (verb, label, closes, predicate_key, rank_class, requires_pr, requires_ref,
   requires_owner, requires_issue, starts_pipeline, wait_days, description)
VALUES
  ('wait_issue', 'wait for an issue', 'predicate', 'issue_closed', 'wait',
   0, 0, 0, 1, 0, NULL,
   'Wait for a tracker issue to close. Closes when the tracker says it did.');

-- What an action is waiting for, when it waits on an issue.
--
-- A link rather than a spec parsed out of the action. Every predicate verb
-- until now read the action's subject pull request; this one reads an issue,
-- and issues attach to *projects* through project_tracker_issue, which is the
-- wrong grain here twice over: a project may track two issues, so a wait
-- through the project would close when either did, and a wait on a public
-- issue that is nobody's project has no project to hang from.
--
-- Same shape as action_pr, because it is the same relationship to a different
-- kind of subject. The foreign key is the point: the row has to exist for the
-- issue to be polled at all, since IssueKeys drives the poll from
-- tracker_issue. A wait on an issue nothing has ever recorded would be false
-- for ever, which is the trap requires_pr exists to prevent, so linking is
-- what creates the row.
--
-- One issue per action. "This is blocked until that closes" is a single fact,
-- and an action waiting on two of them is two waits -- which the blocking
-- graph already expresses, and expresses better, because it can say which
-- arrived first.
CREATE TABLE action_tracker_issue (
  action_id  TEXT PRIMARY KEY REFERENCES action(id),
  issue_id   TEXT NOT NULL REFERENCES tracker_issue(id),
  created_at TEXT NOT NULL
) STRICT;

-- the reverse lookup: which actions are waiting on this issue
CREATE INDEX action_tracker_issue_issue ON action_tracker_issue(issue_id);
