-- The design sketch opens with three pragmas: journal_mode = WAL,
-- foreign_keys = ON and busy_timeout = 5000. They are not here because they
-- are connection settings rather than schema: foreign_keys and busy_timeout
-- reset on every new connection, and database/sql pools them, so setting
-- either from this file would leave most connections without it. The store
-- package applies all three as DSN parameters instead. journal_mode is
-- persistent and only needs setting once, but is asserted the same way.
--
-- Everything below is applied once, inside a transaction, and stamped into
-- PRAGMA user_version. See store.schemaVersion before changing it.

-- ── identity ─────────────────────────────────────────────────────────
CREATE TABLE sequence (
  kind    TEXT PRIMARY KEY,
  next_n  INTEGER NOT NULL
) STRICT;

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
  -- a predicate_key exists exactly when the verb closes on one
  CHECK ((closes = 'predicate') = (predicate_key IS NOT NULL))
) STRICT;

-- ── project ──────────────────────────────────────────────────────────
CREATE TABLE project (
  id               TEXT PRIMARY KEY,           -- 'SL106'
  kind             TEXT NOT NULL DEFAULT 'SL',
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
  jira_key         TEXT,                       -- informal FK into Jira, e.g. 'CDSS-1744'
  jira_status      TEXT,                       -- observed from here down. Jira's vocabulary,
                                               -- not ours: 'To Do' | 'In Progress' | 'Done' | ...
  jira_sprint      TEXT,
  jira_assignee    TEXT,
  jira_synced_at   TEXT,
  last_verified_at TEXT,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  UNIQUE (kind, n),
  CHECK (id = kind || n),
  CHECK ((snooze_until IS NOT NULL) = (status = 'snoozed'))
) STRICT;

-- ── action ───────────────────────────────────────────────────────────
CREATE TABLE action (
  id            TEXT PRIMARY KEY,              -- 'NA37'
  kind          TEXT NOT NULL DEFAULT 'NA',
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

-- ── pr ───────────────────────────────────────────────────────────────
CREATE TABLE pr (
  id                 TEXT PRIMARY KEY,         -- 'saas-infra-plane#4174'
  repo               TEXT NOT NULL,
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
  UNIQUE (repo, number),
  CHECK (id = repo || '#' || number)
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
  --   'project'          -> project.id           e.g. 'SL106'
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
  kind       TEXT NOT NULL CHECK (kind IN ('oncall','pto','holiday','other')),
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
  -- same informal-FK pair as `event`: 'project'->'SL106', 'action'->'NA57'
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
