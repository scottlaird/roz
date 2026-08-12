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
-- Written as one expression, `[prefix/]matcher`, and stored as its two halves.
-- `api/>=3.6` is the api series at 3.6 or later; `>=1.2` is the top-level one.
--
-- The matcher is normally a semver constraint, which is what makes "the next
-- release" expressible: when the block is written nobody knows whether it will
-- be v1.5.0 or v1.5.1, and an item needing a hand correction later is what
-- this replaces. Constraints also settle pre-releases by a published rule
-- rather than a local invention -- `>=1.2` does not match 1.3.0-rc1, and
-- `>=1.2.0-0` does.
--
-- A matcher that is not a constraint is a literal name or a glob, which is the
-- only way to wait on a ref no version scheme describes: a `release-1.5`
-- branch being cut. `release-1.5` is not a parseable constraint, so the two
-- forms do not overlap.
--
-- The path prefix is compared for EQUALITY, never across. An empty prefix
-- means a top-level ref and must not be satisfied by api/v2.3.4: those are
-- different series that happen to share a repository, and comparing their
-- versions to each other is meaningless.
--
-- Keyed on action_id: one action waits for one thing. The same constraint
-- action_one_subject makes for pull requests, for the same reason -- an item
-- covering two unrelated conditions is one somebody should have split.
CREATE TABLE action_ref_wait (
  action_id   TEXT PRIMARY KEY REFERENCES action(id),
  repo_id     TEXT NOT NULL REFERENCES github_repo(id),
  kind        TEXT NOT NULL CHECK (kind IN ('branch','tag')),
  -- everything before the final slash: 'api', 'service/s3', or '' for
  -- top-level. Not NULL -- absent is the empty string, since it is compared.
  path_prefix TEXT NOT NULL,
  -- everything after it: a semver constraint, or a literal name or glob
  matcher     TEXT NOT NULL CHECK (matcher <> ''),
  created_at  TEXT NOT NULL
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
   'Wait for a branch or tag to appear. Closes when one matching the expression exists.');
