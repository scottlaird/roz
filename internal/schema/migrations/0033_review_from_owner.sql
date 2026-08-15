-- A pipeline step that waits for a *particular* group's review.
--
-- wait_review closes on reviewDecision, which is a single verdict for the
-- whole pull request: APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED. A change
-- needing three reviews in order -- your own team, then the owners of code it
-- happens to touch, then whoever guards the protected parts -- cannot be
-- expressed with it at all. A pipeline can say "wait for review" once, and it
-- closes the moment the pull request as a whole is approved, which for a
-- three-stage review is the wrong answer twice.
--
-- The mechanism is the one pipeline_step.spec already provides: a step is a
-- verb, and a spec where the verb needs telling. A release gate says which
-- release; this says which group. What a spec means stays the verb's business,
-- which is why the verb declares what it needs rather than the step declaring
-- what it is.
ALTER TABLE actionverb ADD COLUMN requires_owner INTEGER NOT NULL DEFAULT 0
  CHECK (requires_owner IN (0,1));

INSERT INTO actionverb
  (verb, label, closes, predicate_key, rank_class, requires_pr, requires_ref,
   requires_owner, starts_pipeline, wait_days, description)
VALUES
  ('wait_review_from', 'wait for review from', 'predicate', 'pr_approved_by',
   'wait', 1, 0, 1, 0, 3,
   'Wait for one named group to approve, rather than for the pull request as a whole. Closes when somebody who stands for that group has approved.');

-- Who belongs to a team.
--
-- An approval arrives as a login, and a step waits for a team, so answering
-- "has this group approved" is a question about membership. Every predicate is
-- a pure function of stored rows -- it may not call GitHub -- so the
-- membership it reads has to be here rather than fetched when the question is
-- asked.
--
-- Cached, not authoritative. Membership changes without anything in roz
-- changing, so the read time is what says how much to trust it: sync refreshes
-- a team when the answer is old and something is actually waiting on it, which
-- keeps this to the handful of teams that open steps name rather than every
-- team in the organisation.
--
-- A team nothing has read has no rows, and that reads as "not approved" rather
-- than as "approved" -- absence is not completion, the same rule every other
-- predicate follows. A wait that will not close because membership was never
-- read is visible in the queue; one that closed because it was never read
-- would not be.
--
-- Two tables, because "when was this read" and "who is in it" are different
-- facts and an empty team has only the first. One table keyed on the member
-- could not record that a team was read and found empty, so an empty team
-- would look unread and be fetched again every cycle -- which is exactly the
-- team whose read is least worth repeating.
CREATE TABLE team_membership (
  -- as CODEOWNERS writes it: '@org/storage'
  team      TEXT PRIMARY KEY CHECK (team <> '' AND instr(team, '/') > 0),
  synced_at TEXT NOT NULL
) STRICT;

CREATE TABLE team_member (
  team  TEXT NOT NULL REFERENCES team_membership(team),
  -- as CODEOWNERS writes a person: '@alice'
  login TEXT NOT NULL CHECK (login <> ''),
  PRIMARY KEY (team, login)
) STRICT;
