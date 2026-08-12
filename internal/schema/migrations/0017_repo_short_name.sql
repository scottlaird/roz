-- The name a person uses for a repository when writing about it.
--
-- Prose fills up with pull request references, and the natural way to write
-- one is a short prefix and a number: api#1234. Without somewhere to record
-- what `api` means, the only way to get a link is to write the whole thing
-- out, which is long, easy to mistype, repeated at every mention, and bakes a
-- URL into prose that roz already knows.
--
-- Authored, alongside announce_channel and pipeline: which repository is meant
-- by a word is a decision, not something GitHub can be asked.
--
-- UNIQUE, and nullable so that most repositories can go without one. The whole
-- value of a short name is that it resolves to exactly one repository, so a
-- collision is refused when it is written rather than settled later by a
-- precedence rule nobody would remember. SQLite permits any number of NULLs
-- under a UNIQUE constraint, which is what makes "unset" the ordinary case.
--
-- The CHECK forbids the empty string, since '' would otherwise be a short name
-- that exactly one repository could hold and no prose could ever write. Unset
-- is NULL.
--
-- No slash and no '#', because those are what tell the two reference forms
-- apart: acme/api#1 is a repository and a number, api#1 is a short name and a
-- number, and a short name containing either would make that ambiguous.
ALTER TABLE github_repo ADD COLUMN short_name TEXT
  CHECK (short_name IS NULL OR (
    short_name <> '' AND
    instr(short_name, '/') = 0 AND
    instr(short_name, '#') = 0
  ));

CREATE UNIQUE INDEX github_repo_short_name ON github_repo(short_name)
  WHERE short_name IS NOT NULL;
