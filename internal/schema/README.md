# internal/schema

The database's shape, and nothing that executes against it.

```
schema.sql              hand-written description of the current schema
migrations/
  0001_initial.sql      the schema as first shipped
  0002_github_repo.sql  github_repo, and the pr rebuild that points at it
schema.go               embeds both, and orders the migrations
```

## Which file is real

**The migrations are.** They are the only thing ever executed, including on a
fresh `todo init` — there is no separate "create from scratch" path, so a new
database and an upgraded one cannot end up different.

`schema.sql` is documentation. It is never run except by
`TestSchemaMatchesMigrations`, which builds a database each way and compares
the normalised contents of `sqlite_schema`. That test is what allows the file
to stay hand-written: without it, documentation would rot silently.

It stays hand-written because regenerating it would lose most of the
reasoning. SQLite preserves comments written *inside* a `CREATE` statement but
not *between* statements, so a dump keeps `-- GitHub's vocabularies, NOT ours`
and loses the section headings and the notes above `sequence` and its trigger.

## Adding a migration

1. Write `migrations/000N_short_description.sql`. The number is the value
   `PRAGMA user_version` takes once it has applied, and must be unique;
   gaps are fine, duplicates are an error.
2. Make the same change to `schema.sql`.
3. Run the tests. `TestSchemaMatchesMigrations` fails if the two disagree.

**Never edit a migration once it has been merged.** A database that already
ran it will not pick the change up, and will silently differ from a fresh one.

**Append new columns at the end of their table** in `schema.sql`, which is
where `ALTER TABLE ADD COLUMN` puts them in the stored SQL. Otherwise the two
files describe the same database but do not compare equal.

## What SQLite will not let you do

`ADD COLUMN` works, but not with `NOT NULL` and no default, not `UNIQUE`, and
not for a `STORED` generated column. Changing a type, a `CHECK`, or
nullability is not supported at all. Any of those needs the table rebuilt:
create `<table>_new`, copy the rows, `DROP` the old one, rename. Given how
much this schema leans on `CHECK` constraints, expect to do that often.
`0002` is a worked example.

Two things that will bite during a rebuild, both learned the hard way:

- **`PRAGMA foreign_keys = OFF` is silently ignored inside a transaction.**
  The documented twelve-step procedure starts by disabling foreign keys, and
  that cannot be done from inside the migration's own transaction. The runner
  sets `defer_foreign_keys` instead, which does work there.

- **A self-reference must name the new table, not the old one.** Writing
  `stacked_on TEXT REFERENCES pr(id)` in `pr_new` reads naturally and fails at
  `COMMIT`: dropping the old table counts a deferred violation for every
  copied row that referenced it, and the rename does not clear the count.
  `PRAGMA foreign_key_check` reports nothing, because by then nothing is
  wrong, so the error arrives with no explanation. Reference `pr_new(id)` and
  let the rename rewrite it.

A rebuild also changes how the table is stored: `ALTER TABLE ... RENAME TO`
writes the name quoted, so the result is `CREATE TABLE "pr"`. The equivalence
test strips identifier quotes for that reason.

## Pragmas

There are none in these files. `foreign_keys` and `busy_timeout` are
per-connection and `database/sql` pools connections, so setting them here
would leave most connections without them. The store passes all three as DSN
parameters instead. See `internal/store/store.go`.
