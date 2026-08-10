-- What closing a verb instantiates, held as data rather than in code.
--
-- Closing a `write` action is supposed to produce the actions that follow it:
-- take the pull request out of draft, announce it, wait for review, merge.
-- That chain is not the same everywhere -- a repository nobody reviews for
-- you goes straight from undraft to merge -- so it is a table, joined to from
-- github_repo, rather than a branch on a review_policy enum.
--
-- Steps are still verbs. A pipeline names them in order; it cannot invent a
-- verb, and it cannot say anything about a verb that the vocabulary does not
-- already say. This is the same limit actionverb draws around predicates: the
-- table holds names, never behaviour.

CREATE TABLE action_pipeline (
  -- n orders the table, and the lowest-numbered active pipeline is the
  -- default a newly tracked repository takes. Order is therefore a statement
  -- about which is usual, and adding a pipeline in front of the others is how
  -- that changes.
  n           INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  label       TEXT NOT NULL,
  active      INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
  description TEXT NOT NULL DEFAULT ''
) STRICT;

-- A pipeline's steps, in order.
--
-- A verb may appear more than once -- address_comments can come round again
-- -- so position is what identifies a step, not the verb.
CREATE TABLE pipeline_step (
  pipeline TEXT NOT NULL REFERENCES action_pipeline(name),
  position INTEGER NOT NULL,
  verb     TEXT NOT NULL REFERENCES actionverb(verb),
  PRIMARY KEY (pipeline, position)
) STRICT;

-- The pipelines as seeded. Two, because the difference between them is not
-- expressible by skipping: wait_review closes on an approval that a
-- repository needing no review will never receive, so it has to be absent
-- rather than satisfied.
--
-- Skipping is for steps that are already true when the pipeline is
-- instantiated. undraft is the common one: where pull requests are not
-- created as drafts there is nothing to un-draft, and instantiating an action
-- that is complete before it exists is how a queue fills with noise.
INSERT INTO action_pipeline (n, name, label, description) VALUES
  (1, 'review', 'reviewed',
   'The usual chain: undraft, announce, wait for review, merge.'),
  (2, 'direct', 'no review',
   'For repositories nobody reviews for you. Still announced nowhere and merged by hand.');

INSERT INTO pipeline_step (pipeline, position, verb) VALUES
  ('review', 1, 'undraft'),
  ('review', 2, 'send_for_review'),
  ('review', 3, 'wait_review'),
  ('review', 4, 'merge'),
  ('direct', 1, 'undraft'),
  ('direct', 2, 'merge');

-- github_repo.review_policy becomes a reference to one of them.
--
-- ADD COLUMN carries the foreign key because its default is NULL, which is
-- the one case SQLite allows with foreign keys enabled. Rebuilding the table
-- would be the usual move and is wrong here: pr.repo references it, and
-- dropping the old table counts a deferred violation for every pull request,
-- which the rename does not clear.
ALTER TABLE github_repo ADD COLUMN pipeline TEXT REFERENCES action_pipeline(name);

UPDATE github_repo SET pipeline = CASE review_policy
    WHEN 'required' THEN 'review'
    WHEN 'none'     THEN 'direct'
  END
WHERE review_policy IS NOT NULL;

-- NULL still means unstated, and stays NULL: a repository tracked before
-- anyone said how it is reviewed has not since acquired an opinion.
ALTER TABLE github_repo DROP COLUMN review_policy;
