-- A branch or tag, as GitHub last reported it.
--
-- Waiting for a release has until now been a snooze to a guessed date, which
-- is wrong in both directions: if the release slips the action wakes early and
-- gets re-snoozed, and if it ships early the action sleeps through it. A date
-- was standing in for a condition, and closing on conditions is the one thing
-- this system already knows how to do.
--
-- Observed, written only by sync, like every other thing GitHub knows. A ref
-- appearing is genuinely news — "the v1.5 branch has been cut" — so these are
-- Records and their first sighting is an event, rather than rows written
-- quietly the way pr_check is.
--
-- The commit is stored but nothing reads it yet. It is what ref_contains will
-- need, and recording it now costs nothing: it arrives in the same response
-- that answers whether the ref exists at all.
CREATE TABLE git_ref (
  -- owner/repo@refs/tags/v1.5.0. The full ref path rather than the short name,
  -- because a branch and a tag may share one and the id has to tell them
  -- apart on its own.
  id          TEXT PRIMARY KEY,
  repo_id     TEXT NOT NULL REFERENCES github_repo(id),
  -- the short name, as a person writes it: 'v1.5.0', not 'refs/tags/v1.5.0'
  name        TEXT NOT NULL,
  kind        TEXT NOT NULL CHECK (kind IN ('branch','tag')),
  commit_sha  TEXT NOT NULL,
  -- when roz first saw it, which is not when it was created: a ref that
  -- existed before anything waited on it is first seen the day something did.
  first_seen  TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  UNIQUE (repo_id, kind, name)
) STRICT;

-- The lookup a predicate does: every ref of one kind in one repository.
CREATE INDEX git_ref_repo ON git_ref(repo_id, kind);

-- What an action is waiting for, when it is waiting for a ref.
--
-- The pattern is a glob rather than a literal name, which is the requirement
-- that shapes this table. When the block is written nobody knows whether the
-- next release is v1.5.0 or v1.5.1, and an item that has to be corrected by
-- hand later is precisely the thing this replaces.
--
-- `after` is an exclusive lower bound, and it is what makes "the next one"
-- expressible: pattern alone matches the release that already shipped. Both
-- are needed — a bound with no pattern would match a patch tag, and a patch
-- does not carry the "the previous release finished rolling out" implication
-- that makes this a useful proxy in the first place.
--
-- Keyed on action_id: one action waits for one thing. The same constraint
-- action_one_subject makes for pull requests, for the same reason — an item
-- covering two unrelated conditions is one somebody should have split.
CREATE TABLE action_ref_wait (
  action_id  TEXT PRIMARY KEY REFERENCES action(id),
  repo_id    TEXT NOT NULL REFERENCES github_repo(id),
  kind       TEXT NOT NULL CHECK (kind IN ('branch','tag')),
  -- glob over the short name, e.g. 'v*.*.0'
  pattern    TEXT NOT NULL CHECK (pattern <> ''),
  -- exclusive lower bound on the version in the name, e.g. 'v1.4.7'.
  -- NULL means any name matching the pattern will do.
  after      TEXT,
  created_at TEXT NOT NULL
) STRICT;

-- Sync polls the refs something is actually waiting for, so this is the query
-- that decides what to ask GitHub about.
CREATE INDEX action_ref_wait_repo ON action_ref_wait(repo_id, kind);

-- Whether a verb needs a ref wait to be able to close, mirroring requires_pr.
--
-- Two columns rather than one "requires a subject" flag, because the two are
-- different questions with different remedies: one is answered by
-- `action link-pr` and the other by `action wait-ref`, and an error naming the
-- wrong one is its own small bug.
ALTER TABLE actionverb ADD COLUMN requires_ref INTEGER NOT NULL DEFAULT 0
  CHECK (requires_ref IN (0,1));

-- The verb the whole table exists for.
--
-- wait_days is NULL, deliberately. A release date is not ours to influence and
-- may be months out; there is nobody to chase, so an overdue report would be
-- noise -- and since scottlaird/roz#87 an overdue wait puts an item in the
-- queue, which makes the noise durable rather than merely passing.
INSERT INTO actionverb (verb, label, closes, predicate_key, rank_class,
                        requires_pr, requires_ref, starts_pipeline, wait_days, description)
VALUES
  ('wait_ref', 'wait for a ref', 'predicate', 'ref_exists', 'wait', 0, 1, 0, NULL,
   'Wait for a branch or tag to appear. Closes when one matching the pattern exists.');
