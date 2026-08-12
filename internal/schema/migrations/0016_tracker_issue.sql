-- One tracker becomes many: jira_issue becomes tracker_issue.
--
-- Nothing about the design was ever Jira-specific. A project points at an item
-- in somebody else's tracker, and roz observes that item's status. Only the
-- naming had hardened around one of them, in the table, the entity and the
-- command. Home projects use GitHub issues and the reader for them already
-- exists, so what is missing is a place to put them.
--
-- The shape is unchanged: an entity plus a many-to-many, which 0008 already
-- settled when one piece of work turned out to need two issues. This adds the
-- discriminator that 0008 had no reason to.
--
-- The id is composed, `tracker:key`, the way pr.id is `repo#number`, and the
-- CHECK holds it to that. Composing rather than trusting the keys not to
-- collide: 'CDSS-1744' and 'owner/repo#123' do not collide today, but "these
-- two vocabularies happen not to overlap" is a property of two third parties
-- rather than something this schema can enforce.
--
-- sprint becomes iteration, because it is the same field in both vocabularies
-- under different names -- Jira's sprint, GitHub's milestone -- and a column
-- called sprint that is permanently NULL on every GitHub issue is a worse
-- answer than a neutral name.
--
-- The tracker vocabulary is a CHECK for the reason pr.tracked_because is:
-- consumers branch on it, and a value nothing can read is not useful. github
-- is allowed here before anything can read it, so the schema is not the thing
-- blocking that work.

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
  UNIQUE (tracker, key),
  CHECK (id = tracker || ':' || key)
) STRICT;

CREATE TABLE project_tracker_issue (
  project_id TEXT NOT NULL REFERENCES project(id),
  issue_id   TEXT NOT NULL REFERENCES tracker_issue(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, issue_id)
) STRICT;

INSERT INTO tracker_issue
  (id, tracker, key, summary, status, iteration, assignee, synced_at, created_at, updated_at)
SELECT 'jira:' || id, 'jira', id, summary, status, sprint, assignee, synced_at, created_at, updated_at
FROM jira_issue;

INSERT INTO project_tracker_issue (project_id, issue_id, created_at)
SELECT project_id, 'jira:' || issue_id, created_at
FROM project_jira;

-- The log's subject_id is an identity pointer, not an observation, so it
-- follows the row it names. What the issue *said* at the time -- the old and
-- new values either side of a change -- is history and is left alone.
UPDATE event
   SET subject_type = 'tracker_issue',
       subject_id   = 'jira:' || subject_id
 WHERE subject_type = 'jira_issue';

DROP INDEX project_jira_issue;
DROP TABLE project_jira;
DROP TABLE jira_issue;

CREATE INDEX project_tracker_issue_issue ON project_tracker_issue(issue_id);
