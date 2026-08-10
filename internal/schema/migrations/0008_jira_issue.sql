-- Jira becomes an entity, and a project may carry more than one issue.
--
-- The columns this replaces could hold exactly one key per project, which is
-- one too few: a piece of work legitimately maps to two tickets — "allow
-- scaling up" and "allow scaling down" being the case that found this — and
-- the second was silently dropped on import.
--
-- Three other things follow from the issue being an entity rather than five
-- columns on the project. The summary has somewhere to live, where before it
-- was read from Jira and discarded. An issue can be recorded before or after
-- anything references it, because it no longer depends on a project existing.
-- And the storage stops disagreeing with the command, which already keys on
-- the issue: a Jira issue does not know it is TD106.

CREATE TABLE jira_issue (
  id         TEXT PRIMARY KEY,          -- 'CDSS-1744'; Jira's identifier, not ours
  summary    TEXT NOT NULL DEFAULT '',  -- observed from here down
  -- Jira's vocabulary, NOT ours: 'To Do' | 'In Progress' | 'Done' | 'Blocked' | …
  -- deliberately unconstrained, because Jira may add a value whenever it likes
  -- and a CHECK here would turn someone else's release into a failing ingest
  status     TEXT,
  sprint     TEXT,
  assignee   TEXT,                      -- display name; empty string means unassigned
  synced_at  TEXT,                      -- when Jira was last read for this issue
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

-- Many-to-many in both directions. A project may track several issues; an
-- issue may be tracked by several projects, which was already true of the
-- column this replaces and is worth keeping — two projects watching one epic
-- is reasonable.
CREATE TABLE project_jira (
  project_id TEXT NOT NULL REFERENCES project(id),
  issue_id   TEXT NOT NULL REFERENCES jira_issue(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, issue_id)
) STRICT;

CREATE INDEX project_jira_issue ON project_jira(issue_id);

-- Carry the existing data across. DISTINCT because two projects may already
-- carry the same key, and the issue is now one row whatever claims it. Where
-- they disagree about an issue's status — possible, since each project held
-- its own copy — max() takes one deterministically rather than failing; the
-- next sync corrects it.
INSERT INTO jira_issue (id, summary, status, sprint, assignee, synced_at, created_at, updated_at)
SELECT jira_key,
       '',
       max(jira_status),
       max(jira_sprint),
       max(jira_assignee),
       max(jira_synced_at),
       min(created_at),
       max(updated_at)
  FROM project
 WHERE jira_key IS NOT NULL AND jira_key <> ''
 GROUP BY jira_key;

INSERT INTO project_jira (project_id, issue_id, created_at)
SELECT id, jira_key, created_at
  FROM project
 WHERE jira_key IS NOT NULL AND jira_key <> '';

ALTER TABLE project DROP COLUMN jira_key;
ALTER TABLE project DROP COLUMN jira_status;
ALTER TABLE project DROP COLUMN jira_sprint;
ALTER TABLE project DROP COLUMN jira_assignee;
ALTER TABLE project DROP COLUMN jira_synced_at;
