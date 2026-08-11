-- One project waiting on another, said in the database rather than in prose.
--
-- project.status has accepted 'blocked' since the start, and nothing recorded
-- what it was blocked on: action_blocks is action-to-action, both columns
-- referencing action(id). So "blocked" was a status with no referent, and the
-- dependency lived in a summary or in somebody's head.
--
-- Two things followed, and both were live. A blocked project vanished from
-- the page, which filters to active. And nothing ever unblocked one: closing
-- an action runs a cascade that frees what was waiting, closing a project did
-- not, because that machinery was actions-only.
--
-- Deliberately the same shape as action_blocks, down to the CHECK. It is the
-- same relationship between different rows, and two spellings of it would be
-- one too many.
CREATE TABLE project_blocks (
  blocker_id TEXT NOT NULL REFERENCES project(id),
  blocked_id TEXT NOT NULL REFERENCES project(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (blocker_id, blocked_id),
  CHECK (blocker_id <> blocked_id)
) STRICT;
