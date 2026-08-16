-- Which issues a pull request is against.
--
-- The last missing edge between the two things anybody outside roz sees. A
-- project links to issues, a pull request links to actions, an action links to
-- a project and to the issue it waits on -- and the pull request and the issue
-- were connected only in prose, in a body roz stores and never reads.
--
-- Many-to-many in both directions, for the reason 0008 settled for projects:
-- one pull request can close two issues, and one issue routinely takes three
-- pull requests.
--
-- source is what makes one table workable, and the argument for it is not the
-- authored/observed split -- no link table carries an actor, because who
-- linked is in the event log. It is deletion. sync reconciles: a `Closes #123`
-- edited out of a body takes its row with it, which means sync must be able to
-- delete its own rows and only its own. Scoping that delete by source is the
-- whole mechanism.
--
--   github   GitHub's closingIssuesReferences -- the connection behind
--            auto-close, already parsed by GitHub out of the body, and correct
--            about cross-repository references that a naive scan would get
--            wrong. sync owns these rows entirely.
--   manual   a person or an agent said so. This is the only path for Jira,
--            which nothing reads, and the way a wrong parse gets corrected.
--
-- In the primary key rather than beside it, so the same association can be
-- both observed and asserted: a person who links what GitHub also reports has
-- said something, and sync dropping its row should not silently drop theirs.
CREATE TABLE pr_tracker_issue (
  pr_id      TEXT NOT NULL REFERENCES pr(id),
  issue_id   TEXT NOT NULL REFERENCES tracker_issue(id),
  source     TEXT NOT NULL CHECK (source IN ('github','manual')),
  created_at TEXT NOT NULL,
  PRIMARY KEY (pr_id, issue_id, source)
) STRICT;

-- The reverse direction, which is the question the weekly wrap-up asks:
-- "which issues did pull requests move this week" starts from the issue.
CREATE INDEX pr_tracker_issue_by_issue ON pr_tracker_issue(issue_id);
