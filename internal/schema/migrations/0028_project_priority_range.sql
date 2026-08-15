-- Widens project.priority from 1-4 to 1-9.
--
-- Four bands leave no room to insert one. Reserving the top band for whatever
-- is on fire means everything below it shifts down by one, and with 4 as the
-- floor the two lowest bands have to merge -- spending the distinction the
-- reserved band was meant to create. Nine is not a considered number of
-- bands; it is enough headroom that the next reservation is a renumber rather
-- than another migration.
--
-- The range stays bounded rather than becoming free-form. priority is what
-- `--sort priority` and the status page rank on, and an unbounded integer
-- invites 100 and 1000 as local overrides of a global ordering -- which is
-- rank_pin's job, and is per-action for a reason.
--
-- Nothing is renumbered here. Existing values stay where they are and remain
-- valid, so this can land and the renumbering can be decided separately.
--
-- ── why this edits the schema text rather than rebuilding the table ──
--
-- SQLite cannot alter a CHECK in place, and the usual answer is the rebuild
-- in 0005: create a new table, copy, drop the old, rename. That works for
-- calendar_window because nothing references it. project is referenced five
-- times from four tables -- action.project_id, project_blocks.blocker_id and
-- blocked_id, project_tracker_issue.project_id, and project's own
-- superseded_by and parent_id -- and both orderings of the rebuild fail
-- against a database with real rows in them:
--
--   * copy, DROP TABLE project, rename the new one into place: PRAGMA
--     foreign_key_check passes and COMMIT then fails with FOREIGN KEY
--     constraint failed. Dropping a referenced table queues deferred
--     violations that the check cannot see, because by then the data is
--     consistent again -- only the counter is not.
--
--   * rename project aside first, create the new one, copy, drop the old:
--     ALTER TABLE ... RENAME rewrites the children's REFERENCES clauses to
--     point at project_old, so dropping it orphans all of them. PRAGMA
--     legacy_alter_table = ON is meant to suppress that rewrite and does not
--     take effect here, set inside the transaction or before it.
--
-- The documented fix is to rebuild with foreign_keys OFF outside a
-- transaction, which this runner deliberately cannot do: the pragma is
-- ignored inside a transaction, and running a migration without one trades a
-- constraint failure for the chance of a half-migrated database.
--
-- So the CHECK is edited where it lives. writable_schema permits the update;
-- RESET reparses the schema afterwards, so nothing depends on the connection
-- being reopened. No rows move, no foreign key is touched, and no table stops
-- existing for a moment.
--
-- The hazard is that this is a literal string match: if the stored text stops
-- matching, replace() rewrites nothing, the UPDATE still reports success, and
-- the migration is recorded as applied against a database it did not change.
-- The assertion below is what stops that -- with the pattern deliberately
-- broken it aborts the migration and leaves user_version at 27, rather than
-- stamping 28 on an unchanged schema. Keep the pattern identical to the
-- column as 0001 writes it, whitespace included.
PRAGMA writable_schema = ON;

UPDATE sqlite_schema
   SET sql = replace(sql,
                     'priority         INTEGER CHECK (priority BETWEEN 1 AND 4)',
                     'priority         INTEGER CHECK (priority BETWEEN 1 AND 9)')
 WHERE type = 'table' AND name = 'project';

PRAGMA writable_schema = RESET;

-- Fails the migration, and so the transaction, if the rewrite did not land:
-- the CHECK rejects a 0 count. A temporary table is the only way to raise
-- from plain SQL -- RAISE() exists only inside a trigger.
CREATE TABLE migration_0028_assert (applied INTEGER CHECK (applied = 1));
INSERT INTO migration_0028_assert (applied)
SELECT COUNT(*) FROM sqlite_schema
 WHERE type = 'table' AND name = 'project'
   AND sql LIKE '%priority BETWEEN 1 AND 9%';
DROP TABLE migration_0028_assert;
