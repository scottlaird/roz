-- The owners a repository would rather go to first.
--
-- `roz codeowners` can say who *could* approve a change. It could not say who
-- to *ask*, so the routing stayed folklore: somebody knows that changes here
-- go to this team first, and nothing writes that down.
--
-- A preference, never an assertion. Each hint is checked against what is
-- actually outstanding before it is used, so one that owns nothing in a
-- particular change is skipped rather than asked. That check is what stops a
-- hint becoming a habit nobody revisits -- the team that used to own this and
-- has not for a year drops out of the answer on its own.
--
-- Hints order what CODEOWNERS already requires; they never substitute for it.
-- Routing continues until the rules are satisfied whatever the hints said, so
-- a wrong hint costs an ask in the wrong order rather than an approval that
-- was never needed.
--
-- Ordered, so a sub-table rather than a column: a repository legitimately has
-- several tiers, and "try these, in this order" is the whole content of the
-- preference.
CREATE TABLE repo_owner_hint (
  repo_id  TEXT NOT NULL REFERENCES github_repo(id),
  -- 1 first. Rewritten from scratch when the list is set, so nothing has to
  -- renumber around an insertion.
  position INTEGER NOT NULL,
  -- an owner as CODEOWNERS writes one: '@org/storage', '@alice'. It need not
  -- appear in the file at all -- a team whose members all belong to an owning
  -- team is a usable hint, since the approval it produces satisfies the rule.
  owner    TEXT NOT NULL CHECK (owner <> ''),
  PRIMARY KEY (repo_id, position)
) STRICT;
