-- Settings the database should carry rather than every invocation.
--
-- The Jira host, the project keys worth linking and the name on the page were
-- flags, which meant passing them on every command and leaving anything that
-- reads the database directly — a sqlite3 shell, the MCP server, a future
-- exporter — with no way to know them at all.
--
-- Columns rather than a string->string bag, on the reasoning the rest of the
-- schema follows: an entity with authored columns gets the diff-based event
-- log for free, and a settings change is exactly the sort of thing worth
-- having in the log. It also gets CHECK constraints and real types, where a
-- key-value table gets neither and invites enable_foo = 'true'. The cost is a
-- migration per setting, which for a handful of settings is the right trade.
-- `sequence` is already this shape: a small table keyed by purpose.
--
-- One row, enforced by the CHECK on the key rather than by convention, so
-- there is no such thing as a second configuration to disagree with the first.
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

-- Seeded here rather than by init, so that every database has the row from
-- the moment it has the table and no reader has to handle its absence. An
-- existing database gets the defaults, which are what it behaves like today.
INSERT INTO config (id, created_at, updated_at)
VALUES ('config',
        strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
        strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
