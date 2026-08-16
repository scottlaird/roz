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
  updated_at    TEXT NOT NULL,
  -- how many days after a pull request ends to keep polling it. Days rather
  -- than hours: what it covers is review comments landing after a merge.
  -- Last because ADD COLUMN put it there. See 0029.
  poll_window_days INTEGER NOT NULL DEFAULT 14 CHECK (poll_window_days >= 0)
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
  -- how many CALENDAR days of waiting is reasonable for this verb before it is
  -- worth somebody's attention -- not working days, so at 1 a wait starting on
  -- a Friday is overdue on Saturday. NULL means it never times out, which is
  -- right for the verbs describing your own work: nothing is waiting, so
  -- nothing can be overdue. Tunable with `roz verb set`. See 0013.
  wait_days     INTEGER,
  -- whether the verb needs a ref wait to be able to close, mirroring
  -- requires_pr. Two columns rather than one "requires a subject" flag: the
  -- remedies differ -- `action link-pr` against `action wait-ref` -- and an
  -- error naming the wrong one is its own small bug. Last of the columns
  -- because ADD COLUMN put it there. See 0020.
  requires_ref  INTEGER NOT NULL DEFAULT 0 CHECK (requires_ref IN (0,1)),
  -- whether the verb needs an owner to be able to close: which group's review
  -- this step waits for. A third flag rather than one "requires a subject"
  -- enum, for the reason requires_ref is separate from requires_pr -- the
  -- remedies differ, and the spec means something different to each. Last of
  -- the columns because ADD COLUMN put it there. See 0033.
  requires_owner INTEGER NOT NULL DEFAULT 0 CHECK (requires_owner IN (0,1)),
  -- whether the verb needs a tracker issue to be able to close. Same reason
  -- again: the remedy is `roz action wait-issue`, and an error naming any of
  -- the others would send somebody the wrong way. Last of the columns because
  -- ADD COLUMN put it there. See 0034.
  requires_issue INTEGER NOT NULL DEFAULT 0 CHECK (requires_issue IN (0,1)),
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
INSERT INTO actionverb (verb, label, closes, predicate_key, rank_class, requires_pr, requires_ref, requires_owner, requires_issue, starts_pipeline, wait_days, description) VALUES
  -- Predicate-closed. These flow through on their own.
  ('undraft',          'un-draft',          'predicate', 'pr_not_draft',      'click',   1, 0, 0, 0, 0, NULL,
   'Take the pull request out of draft.'),
  ('send_for_review',  'send for review',   'predicate', 'pr_announced',      'click',   1, 0, 0, 0, 0, NULL,
   'Announce it where reviewers will see it. The announcement is the one signal GitHub cannot supply.'),
  ('wait_review',      'wait for review',   'predicate', 'pr_approved',       'wait',    1, 0, 0, 0, 0, 3,
   'Nothing to do but wait. Closes when the review decision is APPROVED.'),
  ('address_comments', 'address comments',  'predicate', 'pr_threads_clear',  'session', 1, 0, 0, 0, 0, NULL,
   'Deal with review threads. Closes when none are unresolved against the current head.'),
  ('rebase',           'rebase',            'predicate', 'pr_mergeable',      'click',   1, 0, 0, 0, 0, NULL,
   'Bring it up to date. Closes when the merge state is neither BEHIND nor DIRTY.'),
  ('merge',            'merge',             'predicate', 'pr_merged',         'click',   1, 0, 0, 0, 0, 1,
   'One click, once everything else is done.'),
  -- The one verb that waits on something other than a pull request. wait_days
  -- is NULL because a release date is not ours to influence and there is
  -- nobody to chase: since 0019 an overdue wait puts an item in the queue, so
  -- the noise would be durable rather than passing. See 0020.
  ('wait_ref',         'wait for a ref',    'predicate', 'ref_exists',        'wait',    0, 1, 0, 0, 0, NULL,
   'Wait for a branch or tag to appear. Closes when one matching the expression exists.'),

  -- Human-closed. These are the items worth spending attention on, and the
  -- only ones that reach the queue as thinking work.
  ('decide',           'decide',            'human',     NULL,                'decide',  0, 0, 0, 0, 0, NULL,
   'A judgement that has to be made before anything else can move.'),
  ('write',            'write',             'human',     NULL,                'session', 0, 0, 0, 0, 1, NULL,
   'Actual work. Usually ends with a pull request.'),
  ('announce',         'announce',          'human',     NULL,                'click',   0, 0, 0, 0, 0, NULL,
   'Tell someone something.'),
  ('run',              'run',               'human',     NULL,                'click',   0, 0, 0, 0, 0, NULL,
   'Run a command or a job and see what it says.'),
  ('file',             'file',              'human',     NULL,                'click',   0, 0, 0, 0, 0, NULL,
   'Raise a ticket or an issue somewhere else.'),
  ('investigate',      'investigate',       'human',     NULL,                'session', 0, 0, 0, 0, 0, NULL,
   'Find out what is going on. Closes when you know.'),

  -- review is human-closed for now, though the sketch has it closing on a
  -- predicate. It needs to know that *we* submitted a review, which needs
  -- both the viewer's identity and pull requests tracked because they are
  -- assigned to us rather than authored by us. Neither exists yet.
  ('review',           'review',            'human',     NULL,                'session', 1, 0, 0, 0, 0, NULL,
   'Review someone else''s pull request.'),

  -- One named group's review, rather than the pull request as a whole.
  -- reviewDecision is a single verdict and cannot express a change that needs
  -- three reviews in order, so this asks about the group its spec names. Its
  -- allowance matches wait_review: a tier is still one team being waited on.
  -- See 0033.
  ('wait_review_from', 'wait for review from', 'predicate', 'pr_approved_by',  'wait',    1, 0, 1, 0, 0, 3,
   'Wait for one named group to approve, rather than for the pull request as a whole. Closes when somebody who stands for that group has approved.'),

  -- Waiting on work that is not ours. wait_days is NULL for wait_ref's reason:
  -- somebody else's issue is not ours to chase, and an overdue wait puts an
  -- item in the queue that would never go away. See 0034.
  ('wait_issue',       'wait for an issue',  'predicate', 'issue_closed',    'wait',    0, 0, 0, 1, 0, NULL,
   'Wait for a tracker issue to close. Closes when the tracker says it did.');

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
  -- what this step waits for, where the verb needs telling: a release gate has
  -- to say which release. Opaque here -- what a spec means is the verb's
  -- business, the same limit actionverb draws around predicates. Last of the
  -- columns because ADD COLUMN put it there. See 0023.
  spec     TEXT,
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
  id               TEXT PRIMARY KEY,           -- 'ROZ106' with the default prefix
  kind             TEXT NOT NULL,              -- = sequence.kind for 'project'
  n                INTEGER NOT NULL,
  title            TEXT NOT NULL,
  summary          TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL CHECK (status IN
                     ('active','blocked','snoozed','done','retired','superseded')),
  priority         INTEGER CHECK (priority BETWEEN 1 AND 9),  -- see 0028
  effort           TEXT CHECK (effort IN ('minutes','hours','session','days','weeks')),
  snooze_until     TEXT,
  snooze_reason    TEXT NOT NULL DEFAULT '',
  superseded_by    TEXT REFERENCES project(id),
  design_refs      TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(design_refs)),  -- JSON array of file paths
  last_verified_at TEXT,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  -- the project this is part of, for display only: a parent does not block a
  -- child, closing one does not close the other, and nothing about ranking
  -- reads it. project_blocks already says one project must finish before
  -- another, and conflating "is part of" with "waits for" would make both mean
  -- less. Most projects have none. The CHECK stops the shortest cycle; longer
  -- ones cannot be expressed over one row, so the command walks the ancestors.
  -- Last of the columns because ADD COLUMN put it there. See 0026.
  parent_id        TEXT REFERENCES project(id)
                     CHECK (parent_id IS NULL OR parent_id <> id),
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
  -- observed: when this was last checked against reality, as distinct from
  -- updated_at, which every write moves. Written only by `roz verify`, which
  -- the sketch groups with sync as a writer of observed fields. Last because
  -- ADD COLUMN put it there. See 0011.
  last_verified_at TEXT,
  -- authored: when it stops being reasonable to still be waiting on this one.
  -- NULL means the verb's wait_days, read when the deadline is checked rather
  -- than copied here. Last because ADD COLUMN put it there. See 0013.
  okay_to_wait_until TEXT,
  -- authored: when this last became something a person could act on --
  -- un-hidden, or freed by its last blocker. NULL means since it was created.
  -- The deadline takes the latest of this, created_at and waiting_since, so a
  -- step cannot be late before it was actionable. Last because ADD COLUMN put
  -- it there. See 0021.
  ready_since TEXT,
  -- authored: which of the requested reviewers this wait is actually for.
  -- waiting_on beside it is observed and holds everyone GitHub says was
  -- asked, which is routinely several teams pulled in by incidental files;
  -- this is the judgement of whose approval unblocks the work, which GitHub
  -- has no way of knowing. Both are true, and this is usually but not always
  -- one of that list. Last because ADD COLUMN put it there. See 0031.
  waiting_for TEXT CHECK (waiting_for IS NULL OR waiting_for <> ''),
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
  id                TEXT PRIMARY KEY,        -- 'scottlaird/roz'
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

  -- The name a person uses for this repository when writing about it, so prose
  -- can say api#1234 rather than spelling out a URL. Authored: which
  -- repository a word means is a decision. UNIQUE and nullable -- the value is
  -- that it resolves to exactly one repository, and most repositories have
  -- none. No slash and no '#', which are what tell acme/api#1 and api#1 apart.
  -- Last because ADD COLUMN put it there. See 0017.
  short_name        TEXT
    CHECK (short_name IS NULL OR (
      short_name <> '' AND
      instr(short_name, '/') = 0 AND
      instr(short_name, '#') = 0
    )),
  UNIQUE (owner, name),
  CHECK (id = owner || '/' || name)
) STRICT;

-- ── pr ───────────────────────────────────────────────────────────────
CREATE TABLE pr (
  id                 TEXT PRIMARY KEY,         -- 'scottlaird/roz#11'
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
  -- authored, and the second one: why this is tracked at all. The row's
  -- existence records the decision, not the reason, and the two are different
  -- questions once you track something you did not write. A closed set
  -- because every consumer branches on it. NULL means unstated. See 0015.
  tracked_because    TEXT
                       CHECK (tracked_because IS NULL OR
                              tracked_because IN ('authored','reviewing','watching')),
  -- who this needed, worked out from the files it touches against the
  -- repository's CODEOWNERS. An observation with a time: ownership changes
  -- underneath a long-lived pull request, so the log carries when each answer
  -- was true. The set of owners, not the mapping of every path to its owner --
  -- a large change names half an organisation, and "who owns line 40 of the
  -- generated mock" is a live read away rather than a column of noise. NULL
  -- Empty is ambiguous alone -- owned by nobody, or never worked out -- and
  -- owners_head is what tells them apart.
  required_owners    TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(required_owners)),
  -- the head it was worked out against, which is what makes re-deriving
  -- skippable: the files a pull request touches change only when it does. Not
  -- last_synced_at, which moves on every poll. See 0024.
  owners_head        TEXT,
  -- when GitHub says it merged, which is not when roz noticed. The log holds
  -- the second and it is the wrong answer for "what did I merge this week":
  -- a pull request tracked after the fact transitions to MERGED at the poll
  -- that caught up, or never, having been merged the first time it was read.
  -- NULL means not merged. See 0027.
  merged_at          TEXT,
  -- when GitHub says it closed, whether by merging or not. It is what the
  -- poll window is measured from: merged_at dates a merge and says nothing
  -- about a close, so a window keyed off it alone would hold every CLOSED row
  -- either permanently in or permanently out. NULL means still open, or not
  -- read since the column existed. See 0029.
  closed_at          TEXT,
  -- the branch it is from. base_ref is the branch it targets, and without
  -- this there was nothing to match one against the other -- which is why
  -- stacked_on was never set. See 0030.
  head_ref           TEXT,
  UNIQUE (repo, number),
  CHECK (id = repo || '#' || number)
) STRICT;

-- ── trackers ─────────────────────────────────────────────────────────
-- Its own entity rather than columns on project, because one piece of work
-- legitimately maps to more than one issue and a column can hold one key.
CREATE TABLE tracker_issue (
  id         TEXT PRIMARY KEY,          -- 'jira:CDSS-1744' | 'github:owner/repo#123'
  tracker    TEXT NOT NULL CHECK (tracker IN ('jira','github')),
  key        TEXT NOT NULL,             -- the tracker's own identifier, as it writes it
  summary    TEXT NOT NULL DEFAULT '',  -- observed from here down
  -- The tracker's vocabulary, NOT ours: 'To Do' | 'In Progress' | 'open' | ...
  -- deliberately unconstrained, because a tracker may add a value whenever it
  -- likes and a CHECK here would turn someone else's release into a failing
  -- ingest.
  status     TEXT,
  iteration  TEXT,                      -- Jira's sprint, GitHub's milestone
  assignee   TEXT,                      -- display name; empty string means unassigned
  synced_at  TEXT,                      -- when the tracker was last read for this issue
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  -- when the tracker says it closed, for the same reason pr.merged_at exists.
  -- NULL means not closed, or -- for Jira, which nothing reads -- that nobody
  -- has said; status is what answers whether it is open. See 0027.
  closed_at  TEXT,
  UNIQUE (tracker, key),
  CHECK (id = tracker || ':' || key)
) STRICT;

-- ── edges ────────────────────────────────────────────────────────────
CREATE TABLE action_blocks (
  blocker_id TEXT NOT NULL REFERENCES action(id),
  blocked_id TEXT NOT NULL REFERENCES action(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (blocker_id, blocked_id),
  CHECK (blocker_id <> blocked_id)
) STRICT;

-- A row per check rather than the whole map in one column, so the diff says
-- which check moved instead of "the map changed". See 0012 for why, and
-- store/prcheck.go for which transitions are worth an event.
--
-- state is GitHub's vocabulary and deliberately unconstrained, like
-- review_decision and merge_state_status.
CREATE TABLE pr_check (
  pr_id       TEXT NOT NULL REFERENCES pr(id),
  name        TEXT NOT NULL,   -- the context name, as GitHub reports it
  state       TEXT NOT NULL,   -- SUCCESS | FAILURE | PENDING | SKIPPED | ...
  observed_at TEXT NOT NULL,
  PRIMARY KEY (pr_id, name)
) STRICT;

-- One project waiting on another. The same shape as action_blocks, because it
-- is the same relationship between different rows. See 0014.
CREATE TABLE project_blocks (
  blocker_id TEXT NOT NULL REFERENCES project(id),
  blocked_id TEXT NOT NULL REFERENCES project(id),
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
CREATE TABLE project_tracker_issue (
  project_id TEXT NOT NULL REFERENCES project(id),
  issue_id   TEXT NOT NULL REFERENCES tracker_issue(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, issue_id)
) STRICT;

-- an action has at most ONE subject PR: this is the constraint that makes
-- "one item covering two unrelated PRs" unrepresentable rather than merely wrong
CREATE UNIQUE INDEX action_one_subject ON action_pr(action_id) WHERE role = 'subject';

-- What an action is waiting for, when it waits on an issue. The same shape as
-- action_pr, because it is the same relationship to a different kind of
-- subject -- and not through project_tracker_issue, which is the wrong grain
-- twice: a project may track two issues, so a wait through the project would
-- close when either did, and an issue that is nobody's project has no project
-- to hang from.
--
-- The foreign key is the point. IssueKeys drives the poll from tracker_issue,
-- so linking is what makes the issue polled at all -- a wait on an issue
-- nothing has recorded would be false for ever. One issue per action: waiting
-- on two of them is two waits, which the blocking graph expresses better,
-- since it can say which arrived first. See 0034.
CREATE TABLE action_tracker_issue (
  action_id  TEXT PRIMARY KEY REFERENCES action(id),
  issue_id   TEXT NOT NULL REFERENCES tracker_issue(id),
  created_at TEXT NOT NULL
) STRICT;

-- The owners a repository would rather go to first.
--
-- A preference, never an assertion: each is checked against what is actually
-- outstanding in a given change before it is used, so a hint that owns nothing
-- there is skipped rather than asked. Hints order what CODEOWNERS already
-- requires and never substitute for it.
--
-- Ordered, so a sub-table rather than a column -- a repository legitimately has
-- several tiers, and "try these, in this order" is the whole content of it. An
-- owner need not appear in CODEOWNERS: a team whose members all belong to an
-- owning team is usable, since the approval it produces satisfies the rule.
-- See 0025.
CREATE TABLE repo_owner_hint (
  repo_id  TEXT NOT NULL REFERENCES github_repo(id),
  position INTEGER NOT NULL,
  owner    TEXT NOT NULL CHECK (owner <> ''),
  PRIMARY KEY (repo_id, position)
) STRICT;

-- ── git refs ─────────────────────────────────────────────────────────
-- A branch or tag as GitHub last reported it. Observed, written only by sync.
--
-- Waiting for a release was a snooze to a guessed date, which is wrong in both
-- directions -- early and the action gets re-snoozed, late and it sleeps
-- through the thing it was waiting for. A date standing in for a condition.
--
-- These are Records and their first sighting is an event, unlike pr_check,
-- because a ref appearing is news: "the v1.5 branch has been cut" is the fact
-- somebody was waiting for. commit_sha is stored and nothing reads it yet --
-- it is what ref_contains will want, and it arrives in the same response that
-- answers whether the ref exists at all. See 0020.
CREATE TABLE git_ref (
  -- owner/repo@refs/tags/v1.5.0. The full ref path rather than the short name,
  -- because a branch and a tag may share one and the id has to tell them apart
  -- on its own.
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

-- What an action is waiting for, when it is waiting for a ref.
--
-- Written as one expression, `[prefix/]matcher`, and stored as its two halves:
-- `api/>=3.6` is the api series at 3.6 or later, `>=1.2` the top-level one.
--
-- The matcher is normally a semver constraint, which is what makes "the next
-- release" expressible before anybody knows its number, and what settles
-- pre-releases by a published rule rather than a local invention: `>=1.2` does
-- not match 1.3.0-rc1 and `>=1.2.0-0` does. A matcher that does not parse as a
-- constraint is a literal name or glob, which is the only way to wait on a ref
-- no version scheme describes -- a `release-1.5` branch being cut.
--
-- The path prefix is compared for EQUALITY, never across. An empty prefix is a
-- top-level ref and must not be satisfied by api/v2.3.4: different series that
-- share a repository, whose versions mean nothing to each other.
--
-- Keyed on action_id: one action waits for one thing, the same constraint
-- action_one_subject makes for pull requests and for the same reason.
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

-- A ref wait whose version is not decided yet.
--
-- A release gate in a pipeline is written relative to wherever the repository
-- has got to -- "block until two minors on from here" -- and the number that
-- is relative to has to be read before it can be worked out. Resolving while
-- closing an action would mean network I/O in the cascade, and closing is a
-- database operation that should not fail because GitHub is slow.
--
-- So a step instantiates into here, and the next sync -- which polls this
-- repository *because* of this row -- reads the tags, works out the bound,
-- writes the real wait and deletes this. It resolves once and is then frozen:
-- a bound re-derived every poll would move its own goalposts, pushed out by
-- each release that shipped, and the gate would never open.
--
-- Two tables rather than a nullable column on action_ref_wait, because these
-- are two states rather than one with a hole in it. The transition is one-way,
-- and a predicate reading action_ref_wait cannot see a wait that means nothing
-- yet. See 0023.
CREATE TABLE action_ref_pending (
  action_id  TEXT PRIMARY KEY REFERENCES action(id),
  repo_id    TEXT NOT NULL REFERENCES github_repo(id),
  kind       TEXT NOT NULL CHECK (kind IN ('branch','tag')),
  -- the relative expression, as written on the step: [prefix/]component+n
  spec       TEXT NOT NULL CHECK (spec <> ''),
  created_at TEXT NOT NULL
) STRICT;

-- ── the page's own prose ─────────────────────────────────────────────

-- Authored prose the page places, keyed by slot rather than attached to an
-- item. Distinct from `roz note`, which puts a comment in the log against a
-- subject. The key comes from a closed set the renderer knows; see 0018 for
-- why that set is not a CHECK.
CREATE TABLE page_note (
  key        TEXT PRIMARY KEY,
  body       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

-- The action an exception raised, and the condition it was raised for, so that
-- one outstanding condition yields one item in the queue however many times it
-- fires. The subject pair is the informal FK `event` uses. See 0019.
CREATE TABLE raised_action (
  action_id    TEXT PRIMARY KEY REFERENCES action(id),
  kind         TEXT NOT NULL,   -- the exception kind, e.g. 'waited_too_long'
  subject_type TEXT NOT NULL,
  subject_id   TEXT NOT NULL,
  created_at   TEXT NOT NULL
) STRICT;

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
  --   'project'          -> project.id           e.g. 'ROZ106'
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
  -- same informal-FK pair as `event`: 'project'->'ROZ106', 'action'->'NA57'
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

-- ── owner_channel ────────────────────────────────────────────────────
-- Where a group is reached. The channel belongs to the reviewer rather than
-- to the repository: a change touching storage should reach the storage
-- channel whichever repository it is in, and a monorepo has no single right
-- answer at all. Once routing has decided who to ask, this is a table read.
--
-- Groups only -- the '/' in the key is what makes '@org/storage' a key and
-- '@alice' not one. A person is reached by naming them, and where routing
-- resolves to a person this has no answer, which is better said than
-- invented. See 0032.
CREATE TABLE owner_channel (
  owner      TEXT PRIMARY KEY CHECK (owner <> '' AND instr(owner, '/') > 0),
  channel    TEXT NOT NULL CHECK (channel <> ''),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

-- ── team_membership, team_member ─────────────────────────────────────
-- Who belongs to a team. An approval arrives as a login and a step waits for a
-- team, so "has this group approved" is a question about membership -- and a
-- predicate is a pure function of stored rows, so the membership it reads has
-- to be here rather than fetched when the question is asked.
--
-- Cached rather than authoritative: membership changes without anything in roz
-- changing, and the read time is what says how much to trust it. Sync
-- refreshes a team only when something is actually waiting on it, which keeps
-- this to the handful of teams open steps name. A team nothing has read has no
-- rows, which reads as "not approved" -- absence is not completion.
--
-- Two tables because an empty team has a read time and no members, and one
-- table keyed on the member could not say that it had been read. See 0033.
CREATE TABLE team_membership (
  team      TEXT PRIMARY KEY CHECK (team <> '' AND instr(team, '/') > 0),
  synced_at TEXT NOT NULL
) STRICT;

CREATE TABLE team_member (
  team  TEXT NOT NULL REFERENCES team_membership(team),
  login TEXT NOT NULL CHECK (login <> ''),
  PRIMARY KEY (team, login)
) STRICT;

-- ── view ─────────────────────────────────────────────────────────────
-- A listing you can name and come back to: which listing, which columns,
-- which order, which filter. Everything it holds already exists as a flag;
-- what it adds is a name, so a question worth asking twice does not have to
-- be retyped.
--
-- The point is that adding one needs no code — a filter is an expression over
-- columns a listing already declares, so a new question is a row rather than
-- a flag, a query and a release.
--
-- Named rather than numbered, because a view is referred to by what it is for
-- and not as one of many similar things. entity is unconstrained here: which
-- listings exist is a fact about the command tree, and a CHECK naming them
-- would be a second place to edit. See 0035.
CREATE TABLE view (
  -- a lower-case identifier, `^[a-z][a-z0-9_]*$`: the characters that survive
  -- a shell without quoting. GLOB rather than a regex, which SQLite lacks.
  name       TEXT PRIMARY KEY CHECK (
               name GLOB '[a-z]*' AND NOT name GLOB '*[^a-z0-9_]*'),
  entity     TEXT NOT NULL CHECK (entity <> ''),
  filter     TEXT NOT NULL DEFAULT '',
  sort       TEXT NOT NULL DEFAULT '',
  fields     TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

-- ── indexes for the queries that run every render ────────────────────
CREATE INDEX action_open     ON action(state, verb) WHERE closed_at IS NULL;
CREATE INDEX action_expired  ON action(snooze_until)
                             WHERE state = 'snoozed' AND snooze_until IS NOT NULL;
CREATE INDEX project_expired ON project(snooze_until)
                             WHERE status = 'snoozed' AND snooze_until IS NOT NULL;
CREATE INDEX action_project  ON action(project_id);
CREATE INDEX project_tracker_issue_issue ON project_tracker_issue(issue_id);
-- reading a tree walks parent to children, so that is the direction to serve
CREATE INDEX project_parent ON project(parent_id) WHERE parent_id IS NOT NULL;
CREATE UNIQUE INDEX github_repo_short_name ON github_repo(short_name)
  WHERE short_name IS NOT NULL;
CREATE INDEX raised_action_condition
  ON raised_action(kind, subject_type, subject_id);
-- the lookup a ref predicate does: every ref of one kind in one repository
CREATE INDEX git_ref_repo ON git_ref(repo_id, kind);
-- sync polls the refs something is actually waiting for, so this is what
-- decides which repositories it asks about
CREATE INDEX action_ref_wait_repo ON action_ref_wait(repo_id, kind);
-- sync polls the repositories it has something to resolve for, as well as the
-- ones something is already waiting on
CREATE INDEX action_ref_pending_repo ON action_ref_pending(repo_id, kind);
-- the week in review: a window over what finished, ordered by when it did.
-- Partial, because what has not finished is most of both tables and is never
-- what either query is asking for.
CREATE INDEX pr_merged_at ON pr(merged_at) WHERE merged_at IS NOT NULL;
CREATE INDEX tracker_issue_closed_at ON tracker_issue(closed_at) WHERE closed_at IS NOT NULL;
-- the poll set: open pull requests plus the ones that ended recently
CREATE INDEX pr_poll_window ON pr(closed_at) WHERE closed_at IS NOT NULL;
-- which actions are waiting on this issue
CREATE INDEX action_tracker_issue_issue ON action_tracker_issue(issue_id);
-- the listing's own question: which views can it offer
CREATE INDEX view_entity ON view(entity);
-- given a base_ref, which tracked pull request in the repository has that head
CREATE INDEX pr_head_ref ON pr(repo, head_ref) WHERE head_ref IS NOT NULL;
