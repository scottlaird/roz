-- Adds github_repo, and points pr.repo at it.
--
-- A repository carries policy a pull request cannot: whether review is
-- required at all, where its pull requests get announced, and which branch is
-- the default -- the last of which is what stacked_on is defined against.

CREATE TABLE github_repo (
  id                TEXT PRIMARY KEY,        -- 'scottlaird/roz'
  owner             TEXT NOT NULL,
  name              TEXT NOT NULL,

  -- authored. review_policy is a judgement, not an observation: reading
  -- branch protection needs admin on the repository, so it is unavailable
  -- exactly where the repository is not yours. NULL means unstated.
  review_policy     TEXT CHECK (review_policy IN ('required','none')),
  announce_channel  TEXT,                    -- Slack channel for its pull requests
  disposition       TEXT NOT NULL DEFAULT '',

  -- observed from here down
  default_branch    TEXT,                    -- what base_ref is compared against
  uses_merge_queue  INTEGER CHECK (uses_merge_queue IN (0,1)),
  is_archived       INTEGER CHECK (is_archived IN (0,1)),
  raw               TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(raw)),

  tracked_since     TEXT NOT NULL,
  last_synced_at    TEXT,
  UNIQUE (owner, name),
  CHECK (id = owner || '/' || name)
) STRICT;

-- Any repository already named by a tracked pull request is tracked too, so
-- the foreign key below has something to point at. A repo name without an
-- owner cannot be split, and is left for foreign_key_check to reject rather
-- than guessed at.
INSERT INTO github_repo (id, owner, name, tracked_since)
SELECT DISTINCT
    repo,
    substr(repo, 1, instr(repo, '/') - 1),
    substr(repo, instr(repo, '/') + 1),
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM pr
WHERE instr(repo, '/') > 0;

-- pr.repo gains a foreign key, which SQLite cannot add in place: the table
-- has to be rebuilt. Foreign keys are deferred for the duration -- the
-- pragma that disables them outright is ignored inside a transaction -- and
-- the runner checks them before commit.
--
-- stacked_on references pr_new, not pr, and the rename rewrites it. Pointing
-- it at the old pr instead looks more natural and fails at COMMIT: dropping
-- the old table counts a deferred violation for every copied row that
-- referenced it, and renaming does not clear the count. PRAGMA
-- foreign_key_check sees nothing wrong, because by then nothing is.
CREATE TABLE pr_new (
  id                 TEXT PRIMARY KEY,         -- 'scottlaird/roz#11'
  repo               TEXT NOT NULL REFERENCES github_repo(id),
  number             INTEGER NOT NULL,
  title              TEXT NOT NULL DEFAULT '',
  author             TEXT,
  url                TEXT,
  state              TEXT CHECK (state IN ('OPEN','MERGED','CLOSED')),
  is_draft           INTEGER CHECK (is_draft IN (0,1)),
  -- GitHub's vocabularies, NOT ours -- deliberately unconstrained
  --   review_decision:    APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED | NULL
  --   merge_state_status: CLEAN | BLOCKED | BEHIND | DIRTY | UNSTABLE | DRAFT | ...
  --   checks_state:       SUCCESS | FAILURE | PENDING | ... (rollup of `checks`)
  review_decision    TEXT,
  merge_state_status TEXT,
  checks_state       TEXT,
  base_ref           TEXT,                     -- default branch, or a branch name when stacked
  head_sha           TEXT,
  in_merge_queue     INTEGER CHECK (in_merge_queue IN (0,1)),
  checks             TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(checks)),
  reviewer_teams     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(reviewer_teams)),  -- JSON array of team slugs
  approvals          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(approvals)),       -- JSON array of logins
  first_review_requested_at TEXT,
  human_commented_at TEXT,
  announced_at       TEXT,
  announced_channel  TEXT,
  frozen             INTEGER GENERATED ALWAYS AS
                       (human_commented_at IS NOT NULL OR announced_at IS NOT NULL) STORED,
  stacked_on         TEXT REFERENCES pr_new(id),
  raw                TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(raw)),
  tracked_since      TEXT NOT NULL,
  last_synced_at     TEXT,
  UNIQUE (repo, number),
  CHECK (id = repo || '#' || number)
) STRICT;

-- frozen is generated, so it is not copied.
INSERT INTO pr_new (
  id, repo, number, title, author, url, state, is_draft,
  review_decision, merge_state_status, checks_state, base_ref, head_sha,
  in_merge_queue, checks, reviewer_teams, approvals,
  first_review_requested_at, human_commented_at, announced_at, announced_channel,
  stacked_on, raw, tracked_since, last_synced_at
)
SELECT
  id, repo, number, title, author, url, state, is_draft,
  review_decision, merge_state_status, checks_state, base_ref, head_sha,
  in_merge_queue, checks, reviewer_teams, approvals,
  first_review_requested_at, human_commented_at, announced_at, announced_channel,
  stacked_on, raw, tracked_since, last_synced_at
FROM pr;

DROP TABLE pr;
ALTER TABLE pr_new RENAME TO pr;
