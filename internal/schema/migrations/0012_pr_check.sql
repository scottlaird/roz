-- A row per check, rather than the whole map in one column.
--
-- pr.checks held every context's conclusion as JSON, so the diff could only
-- ever say "the map changed". Every job starting or finishing re-emitted all
-- of it: two pull requests produced about fifteen `checks` events in an hour
-- and not one of them said anything worth reading. The signal being acted on
-- was always checks_state, the rollup, which stays where it is.
--
-- Summarising the blob to counts would not have fixed it either. What is
-- worth having is the failing check's *name*, and counts still need a
-- follow-up query to get at it.
--
-- A row per check also makes per-check questions askable at all. Whether a
-- required check passed or merely skipped is not something the database can
-- be asked while the answer is inside one TEXT column.
--
-- state is GitHub's vocabulary, not ours, and deliberately unconstrained for
-- the same reason review_decision and merge_state_status are: GitHub may add
-- a value whenever it likes, and a CHECK here would turn someone else's
-- release into a failing ingest.
CREATE TABLE pr_check (
  pr_id       TEXT NOT NULL REFERENCES pr(id),
  name        TEXT NOT NULL,   -- the context name, as GitHub reports it
  state       TEXT NOT NULL,   -- SUCCESS | FAILURE | PENDING | SKIPPED | ...
  observed_at TEXT NOT NULL,
  PRIMARY KEY (pr_id, name)
) STRICT;

-- Carry the blob across. json_each turns the object into rows; a pull request
-- whose checks are '{}' contributes none, which is correct — it has none.
--
-- observed_at is when the row was last written, and the best available answer
-- for rows that predate it is when the pull request was last synced.
INSERT INTO pr_check (pr_id, name, state, observed_at)
SELECT pr.id, kv.key, kv.value, coalesce(pr.last_synced_at, pr.tracked_since)
  FROM pr, json_each(pr.checks) kv;

ALTER TABLE pr DROP COLUMN checks;
