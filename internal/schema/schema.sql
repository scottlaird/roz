-- The design sketch opens with three pragmas: journal_mode = WAL,
-- foreign_keys = ON and busy_timeout = 5000. They are not here because they
-- are connection settings rather than schema: foreign_keys and busy_timeout
-- reset on every new connection, and database/sql pools them, so setting
-- either from this file would leave most connections without it. The store
-- package applies all three as DSN parameters instead. journal_mode is
-- persistent and only needs setting once, but is asserted the same way.
--
-- This file is documentation, not the thing that runs. The database is built
-- by the numbered files in migrations/, and TestSchemaMatchesMigrations
-- compares the two, so they cannot drift.
--
-- Editing it means writing a matching migration. Append new columns at the
-- end of their table, which is where ALTER TABLE ADD COLUMN puts them, so the
-- two stay textually comparable.

-- ── the migration record ─────────────────────────────────────────────
-- Which migrations a database has run. Created by the runner rather than by
-- a numbered migration, since it is what the numbering is read against.
--
-- It exists because PRAGMA user_version is only a high-water mark: a
-- migration numbered below one already applied would be skipped and never
-- noticed, which is what happens when two branches each add one and land in
-- the other order.
CREATE TABLE IF NOT EXISTS applied_migration (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at TEXT NOT NULL
) STRICT;

-- ── identity ─────────────────────────────────────────────────────────
-- The registry of identifier namespaces as well as the counter. `entity` is
-- ours and fixed; `kind` is the user's, chosen at init, and is the only place
-- the prefix is written down.
--
-- next_n is the next number to hand out, so allocation returns the value from
-- BEFORE the increment:
--   UPDATE sequence SET next_n = next_n + 1 WHERE entity = ?
--     RETURNING next_n - 1, kind;
CREATE TABLE sequence (
  entity  TEXT PRIMARY KEY CHECK (entity IN ('project','action')),
  kind    TEXT NOT NULL UNIQUE,
  next_n  INTEGER NOT NULL CHECK (next_n >= 1)
) STRICT;

-- entity and kind are write-once. Changing a prefix would orphan every
-- identifier already issued -- and those are cited in Jira tickets and in
-- conversation, where nothing here can reach them.
--
-- BEFORE UPDATE OF fires only when a listed column appears in the SET clause,
-- so the allocating UPDATE above is unaffected.
CREATE TRIGGER sequence_identity_immutable
BEFORE UPDATE OF entity, kind ON sequence
BEGIN
  SELECT RAISE(ABORT, 'sequence.entity and sequence.kind are write-once');
END;

-- ── settings ─────────────────────────────────────────────────────────
-- Columns rather than a string->string bag, so a settings change gets the
-- same diff-based event log, CHECK constraints and typing as everything else.
-- The cost is a migration per setting, which for a handful is the right
-- trade. One row, enforced by the CHECK rather than by convention, and seeded
-- by the migration so no reader has to handle its absence.
CREATE TABLE config (
  id            TEXT PRIMARY KEY CHECK (id = 'config'),
  -- Whose queue this is, for the page's heading. A label and nothing more:
  -- it is not a GitHub login and nothing matches on it.
  owner         TEXT NOT NULL DEFAULT '',
  -- Where a Jira key becomes a link, e.g. https://example.atlassian.net/browse
  jira_base_url TEXT NOT NULL DEFAULT '',
  -- JSON array of project keys worth linking, e.g. ["CDSS"]. Empty links
  -- nothing: the shape of a key also matches UTF-8 and SHA-256.
  jira_prefixes TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(jira_prefixes)),
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
) STRICT;

INSERT INTO config (id, created_at, updated_at)
VALUES ('config',
        strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
        strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

-- ── vocabulary ───────────────────────────────────────────────────────
CREATE TABLE actionverb (
  verb          TEXT PRIMARY KEY,
  label         TEXT NOT NULL,
  closes        TEXT NOT NULL CHECK (closes IN ('human','predicate')),
  predicate_key TEXT,                -- informal FK into the code's predicate registry,
                                     -- e.g. 'pr_merged', 'pr_not_draft', 'pr_approved'
  -- drives both sort order and the render class:
  --   'click' one action  |  'decide' judgement  |  'session' real work  |  'wait' on a person
  rank_class    TEXT NOT NULL CHECK (rank_class IN ('click','decide','session','wait')),
  requires_pr   INTEGER NOT NULL DEFAULT 0 CHECK (requires_pr IN (0,1)),
  active        INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
  description   TEXT NOT NULL DEFAULT '',
  -- whether closing an action with this verb opens the repository's pipeline.
  -- Last of the columns because ADD COLUMN put it there.
  starts_pipeline INTEGER NOT NULL DEFAULT 0 CHECK (starts_pipeline IN (0,1)),
  -- a predicate_key exists exactly when the verb closes on one
  CHECK ((closes = 'predicate') = (predicate_key IS NOT NULL))
) STRICT;

-- The vocabulary as seeded. Rows rather than DDL, but part of what a
-- database holds, so it is described here too and TestSeededVocabulary
-- compares it against the migration.
--
-- It is a starting point, not a fixed set: the table exists so the
-- vocabulary can grow without a deploy, and a database may have gained or
-- retired verbs since. Never delete a row -- closed actions and log entries
-- reference retired verbs. Set active = 0 instead.
INSERT INTO actionverb (verb, label, closes, predicate_key, rank_class, requires_pr, starts_pipeline, description) VALUES
  -- Predicate-closed. These flow through on their own.
  ('undraft',          'un-draft',          'predicate', 'pr_not_draft',      'click',   1, 0,
   'Take the pull request out of draft.'),
  ('send_for_review',  'send for review',   'predicate', 'pr_announced',      'click',   1, 0,
   'Announce it where reviewers will see it. The announcement is the one signal GitHub cannot supply.'),
  ('wait_review',      'wait for review',   'predicate', 'pr_approved',       'wait',    1, 0,
   'Nothing to do but wait. Closes when the review decision is APPROVED.'),
  ('address_comments', 'address comments',  'predicate', 'pr_threads_clear',  'session', 1, 0,
   'Deal with review threads. Closes when none are unresolved against the current head.'),
  ('rebase',           'rebase',            'predicate', 'pr_mergeable',      'click',   1, 0,
   'Bring it up to date. Closes when the merge state is neither BEHIND nor DIRTY.'),
  ('merge',            'merge',             'predicate', 'pr_merged',         'click',   1, 0,
   'One click, once everything else is done.'),

  -- Human-closed. These are the items worth spending attention on, and the
  -- only ones that reach the queue as thinking work.
  ('decide',           'decide',            'human',     NULL,                'decide',  0, 0,
   'A judgement that has to be made before anything else can move.'),
  ('write',            'write',             'human',     NULL,                'session', 0, 1,
   'Actual work. Usually ends with a pull request.'),
  ('announce',         'announce',          'human',     NULL,                'click',   0, 0,
   'Tell someone something.'),
  ('run',              'run',               'human',     NULL,                'click',   0, 0,
   'Run a command or a job and see what it says.'),
  ('file',             'file',              'human',     NULL,                'click',   0, 0,
   'Raise a ticket or an issue somewhere else.'),
  ('investigate',      'investigate',       'human',     NULL,                'session', 0, 0,
   'Find out what is going on. Closes when you know.'),

  -- review is human-closed for now, though the sketch has it closing on a
  -- predicate. It needs to know that *we* submitted a review, which needs
  -- both the viewer's identity and pull requests tracked because they are
  -- assigned to us rather than authored by us. Neither exists yet.
  ('review',           'review',            'human',     NULL,                'session', 1, 0,
   'Review someone else''s pull request.');

-- ── pipelines ────────────────────────────────────────────────────────
-- What closing a verb instantiates. Closing a `write` action produces the
-- actions that follow it, and that chain differs by repository, so it is a
-- table joined to from github_repo rather than a branch on an enum.
--
-- Steps are still verbs: a pipeline names them in order, and can say nothing
-- about a verb the vocabulary does not already say. Same limit actionverb
-- draws around predicates -- names, never behaviour.
CREATE TABLE action_pipeline (
  -- n orders the table, and the lowest-numbered active pipeline is the
  -- default a newly tracked repository takes. Order is a statement about
  -- which is usual; adding one in front of the others is how that changes.
  n           INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  label       TEXT NOT NULL,
  active      INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
  description TEXT NOT NULL DEFAULT ''
) STRICT;

-- A verb may appear more than once -- address_comments can come round again
-- -- so position identifies a step, not the verb.
CREATE TABLE pipeline_step (
  pipeline TEXT NOT NULL REFERENCES action_pipeline(name),
  position INTEGER NOT NULL,
  verb     TEXT NOT NULL REFERENCES actionverb(verb),
  PRIMARY KEY (pipeline, position)
) STRICT;

-- The pipelines as seeded, compared against the migration by
-- TestSeededPipelines.
--
-- Two, because the difference between them is not expressible by skipping:
-- wait_review closes on an approval a repository needing no review will never
-- receive, so it has to be absent rather than satisfied. Skipping is for
-- steps already true at instantiation -- undraft, where pull requests are not
-- created as drafts.
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

-- ── project ──────────────────────────────────────────────────────────
CREATE TABLE project (
  id               TEXT PRIMARY KEY,           -- 'TD106' with the default prefix
  kind             TEXT NOT NULL,              -- = sequence.kind for 'project'
  n                INTEGER NOT NULL,
  title            TEXT NOT NULL,
  summary          TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL CHECK (status IN
                     ('active','blocked','snoozed','done','retired','superseded')),
  priority         INTEGER CHECK (priority BETWEEN 1 AND 4),
  effort           TEXT CHECK (effort IN ('minutes','hours','session','days','weeks')),
  snooze_until     TEXT,
  snooze_reason    TEXT NOT NULL DEFAULT '',
  superseded_by    TEXT REFERENCES project(id),
  design_refs      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(design_refs)),  -- JSON array of file paths
  last_verified_at TEXT,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  UNIQUE (kind, n),
  CHECK (id = kind || n),
  CHECK ((snooze_until IS NOT NULL) = (status = 'snoozed'))
) STRICT;

-- ── action ───────────────────────────────────────────────────────────
CREATE TABLE action (
  id            TEXT PRIMARY KEY,              -- 'NA37' with the default prefix
  kind          TEXT NOT NULL,                 -- = sequence.kind for 'action'
  n             INTEGER NOT NULL,
  title         TEXT NOT NULL,
  verb          TEXT NOT NULL REFERENCES actionverb(verb),
  state         TEXT NOT NULL CHECK (state IN
                  ('ready','blocked','snoozed','done','dropped')),
  project_id    TEXT REFERENCES project(id),
  why           TEXT NOT NULL DEFAULT '',
  hidden_behind TEXT REFERENCES action(id),
  snooze_until  TEXT,
  snooze_reason TEXT NOT NULL DEFAULT '',
  rank_pin      INTEGER,
  waiting_on    TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(waiting_on)),  -- observed; JSON array
                                                                         -- of team slugs or logins
  waiting_since TEXT,                                                       -- observed
  closed_at     TEXT,
  closed_reason TEXT CHECK (closed_reason IN
                  ('completed','superseded','dropped','obsolete')),
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  UNIQUE (kind, n),
  CHECK (id = kind || n),
  CHECK (hidden_behind IS NULL OR hidden_behind <> id),
  CHECK ((closed_at IS NOT NULL) = (state IN ('done','dropped')))
) STRICT;

-- ── github_repo ──────────────────────────────────────────────────────
-- A repository carries policy a pull request cannot: how its pull requests
-- get from written to merged, where they are announced, and which branch is
-- the default -- the last of which is what stacked_on is defined against.
CREATE TABLE github_repo (
  id                TEXT PRIMARY KEY,        -- 'scottlaird/todo'
  owner             TEXT NOT NULL,
  name              TEXT NOT NULL,

  announce_channel  TEXT,                    -- Slack channel for its pull requests
  disposition       TEXT NOT NULL DEFAULT '',

  -- observed from here down
  default_branch    TEXT,                    -- what base_ref is compared against
  uses_merge_queue  INTEGER CHECK (uses_merge_queue IN (0,1)),
  is_archived       INTEGER CHECK (is_archived IN (0,1)),
  raw               TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(raw)),

  tracked_since     TEXT NOT NULL,
  last_synced_at    TEXT,

  -- authored, and a judgement rather than an observation: reading branch
  -- protection needs admin on the repository, so it is unavailable exactly
  -- where the repository is not yours, and "these do not get reviewed" is a
  -- statement about how someone works. NULL means unstated.
  --
  -- Last because ADD COLUMN put it there. It replaced a review_policy enum,
  -- which said whether review happened but not what to do about it.
  pipeline          TEXT REFERENCES action_pipeline(name),
  UNIQUE (owner, name),
  CHECK (id = owner || '/' || name)
) STRICT;

-- ── pr ───────────────────────────────────────────────────────────────
CREATE TABLE pr (
  id                 TEXT PRIMARY KEY,         -- 'scottlaird/todo#11'
  repo               TEXT NOT NULL REFERENCES github_repo(id),
  number             INTEGER NOT NULL,
  title              TEXT NOT NULL DEFAULT '',
  author             TEXT,
  url                TEXT,
  state              TEXT CHECK (state IN ('OPEN','MERGED','CLOSED')),
  is_draft           INTEGER CHECK (is_draft IN (0,1)),
  -- GitHub's vocabularies, NOT ours -- deliberately unconstrained, see note below
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
  stacked_on         TEXT REFERENCES pr(id),
  raw                TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(raw)),
  tracked_since      TEXT NOT NULL,
  last_synced_at     TEXT,
  -- unresolved and not outdated; NULL means never synced. See 0003.
  unresolved_threads INTEGER,
  -- authored, and the only authored column here: everything else about a pull
  -- request is observed, and the decision to track one is the row's existence.
  --
  -- NULL means the repository's, read when the chain is instantiated rather
  -- than copied at track time. Unlike github_repo.pipeline against the
  -- default: a default is a guess and freezing it is protective, a
  -- repository's pipeline is the policy and should reach the pull requests
  -- that never claimed an exception to it. Last because ADD COLUMN put it
  -- there. See 0010.
  pipeline           TEXT REFERENCES action_pipeline(name),
  UNIQUE (repo, number),
  CHECK (id = repo || '#' || number)
) STRICT;

-- ── jira ─────────────────────────────────────────────────────────────
-- Its own entity rather than columns on project, because one piece of work
-- legitimately maps to more than one issue and a column can hold one key.
CREATE TABLE jira_issue (
  id         TEXT PRIMARY KEY,          -- 'CDSS-1744'; Jira's identifier, not ours
  summary    TEXT NOT NULL DEFAULT '',  -- observed from here down
  -- Jira's vocabulary, NOT ours: 'To Do' | 'In Progress' | 'Done' | 'Blocked' | ...
  -- deliberately unconstrained, because Jira may add a value whenever it likes
  -- and a CHECK here would turn someone else's release into a failing ingest
  status     TEXT,
  sprint     TEXT,
  assignee   TEXT,                      -- display name; empty string means unassigned
  synced_at  TEXT,                      -- when Jira was last read for this issue
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

-- ── edges ────────────────────────────────────────────────────────────
CREATE TABLE action_blocks (
  blocker_id TEXT NOT NULL REFERENCES action(id),
  blocked_id TEXT NOT NULL REFERENCES action(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (blocker_id, blocked_id),
  CHECK (blocker_id <> blocked_id)
) STRICT;

CREATE TABLE action_pr (
  action_id TEXT NOT NULL REFERENCES action(id),
  pr_id     TEXT NOT NULL REFERENCES pr(id),
  role      TEXT NOT NULL CHECK (role IN ('subject','context')),
  PRIMARY KEY (action_id, pr_id)
) STRICT;

-- Many-to-many in both directions: a project may track several issues, and an
-- issue may be tracked by several projects -- two projects watching one epic.
CREATE TABLE project_jira (
  project_id TEXT NOT NULL REFERENCES project(id),
  issue_id   TEXT NOT NULL REFERENCES jira_issue(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, issue_id)
) STRICT;

-- an action has at most ONE subject PR: this is the constraint that makes
-- "one item covering two unrelated PRs" unrepresentable rather than merely wrong
CREATE UNIQUE INDEX action_one_subject ON action_pr(action_id) WHERE role = 'subject';

-- ── log — deliberately no foreign keys ───────────────────────────────
CREATE TABLE event (
  seq          INTEGER PRIMARY KEY,            -- rowid: monotonic, and the tiebreak
  at           TEXT NOT NULL,                  -- ISO-8601 UTC, ms precision
  correlation  TEXT NOT NULL,                  -- UUID; groups every event from one command

  -- vocab: 'human' | 'sync:github' | 'sync:jira' | 'sync:slack' | 'agent:claude'
  actor        TEXT NOT NULL,
  -- vocab: 'created' | 'changed' | 'blocked' | 'unblocked' | 'snoozed' | 'woke'
  --        | 'closed' | 'synced' | 'verified' | 'exception'
  kind         TEXT NOT NULL,
  severity     TEXT NOT NULL DEFAULT 'info'
                 CHECK (severity IN ('info','notice','exception')),

  -- informal FK, not enforced (polymorphic). vocab and target:
  --   'project'          -> project.id           e.g. 'TD106'
  --   'action'           -> action.id            e.g. 'NA57'
  --   'pr'               -> pr.id                e.g. 'saas-infra-plane#4174'
  --   'calendar_window'  -> calendar_window.id
  --   'priority'         -> priority.id
  -- subject_type is redundant for prefixed ids, but PRs have no prefix and
  -- filtering by type should not mean parsing strings.
  subject_type TEXT NOT NULL,
  subject_id   TEXT NOT NULL,

  field        TEXT NOT NULL DEFAULT '',       -- a column name on the subject's table; '' for lifecycle events
  old_value    TEXT NOT NULL DEFAULT '',
  new_value    TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT '',
  payload      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload))
) STRICT;

CREATE INDEX event_tail    ON event(seq);
CREATE INDEX event_subject ON event(subject_type, subject_id, seq);

-- ── calendar, priorities, review policy ──────────────────────────────
CREATE TABLE calendar_window (              -- not `window`: reserved in SQLite
  id         TEXT PRIMARY KEY,
  -- Free text. Nothing branches on kind: it is stored, filtered and shown,
  -- while capacity is what decides availability. A CHECK here would mean
  -- rebuilding the table for every new kind. See 0005.
  kind       TEXT NOT NULL,
  label      TEXT NOT NULL,
  starts_on  TEXT NOT NULL,                 -- 'YYYY-MM-DD', inclusive
  ends_on    TEXT NOT NULL,                 -- 'YYYY-MM-DD', INCLUSIVE — see the entry note
  capacity   TEXT NOT NULL CHECK (capacity IN ('none','reduced','full')),
  note       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  CHECK (ends_on >= starts_on)
) STRICT;

CREATE TABLE priority (
  id              TEXT PRIMARY KEY,
  stated_at       TEXT NOT NULL,
  effective_from  TEXT,
  effective_until TEXT,
  body            TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','superseded')),
  created_at      TEXT NOT NULL
) STRICT;

CREATE TABLE priority_target (
  priority_id  TEXT NOT NULL REFERENCES priority(id),
  -- same informal-FK pair as `event`: 'project'->'TD106', 'action'->'NA57'
  subject_type TEXT NOT NULL,
  subject_id   TEXT NOT NULL,
  note         TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (priority_id, subject_type, subject_id)
) STRICT;

CREATE TABLE review_rule (
  id          INTEGER PRIMARY KEY,
  match_repo  TEXT NOT NULL,                 -- repo name, or '*'
  match_path  TEXT,                          -- glob against changed files; NULL = whole repo
  route_to    TEXT NOT NULL,                 -- team slug or Slack channel
  tier        INTEGER NOT NULL DEFAULT 1,    -- higher tiers are requested last
  disposition TEXT NOT NULL DEFAULT '',
  active      INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
  note        TEXT NOT NULL DEFAULT ''
) STRICT;

-- ── indexes for the queries that run every render ────────────────────
CREATE INDEX action_open     ON action(state, verb) WHERE closed_at IS NULL;
CREATE INDEX action_expired  ON action(snooze_until)
                             WHERE state = 'snoozed' AND snooze_until IS NOT NULL;
CREATE INDEX project_expired ON project(snooze_until)
                             WHERE status = 'snoozed' AND snooze_until IS NOT NULL;
CREATE INDEX action_project  ON action(project_id);
CREATE INDEX project_jira_issue ON project_jira(issue_id);
