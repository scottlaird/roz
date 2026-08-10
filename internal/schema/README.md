# internal/schema

The database's shape, and nothing that executes against it.

```
schema.sql              hand-written description of the current schema
migrations/
  0001_initial.sql      the schema as first shipped
  0002_github_repo.sql  github_repo, and the pr rebuild that points at it
  ...
  0006_action_pipeline.sql  the pipelines, and the column on github_repo
schema.go               embeds both, and orders the migrations
```

## Which file is real

**The migrations are.** They are the only thing ever executed, including on a
fresh `todo init` — there is no separate "create from scratch" path, so a new
database and an upgraded one cannot end up different.

`schema.sql` is documentation. It is never run except by two tests, which
build a database each way and compare them: `TestSchemaMatchesMigrations` over
the normalised contents of `sqlite_schema`, and `TestSeededVocabulary` and
`TestSeededPipelines` over the seeded rows, which `sqlite_schema` does not
carry. Those are what allow the file to stay hand-written: without them,
documentation would rot silently.

That is also the rule for putting anything else in it. Content in this file is
only worth having if something checks it — seed data included.

It stays hand-written because regenerating it would lose most of the
reasoning. SQLite preserves comments written *inside* a `CREATE` statement but
not *between* statements, so a dump keeps `-- GitHub's vocabularies, NOT ours`
and loses the section headings and the notes above `sequence` and its trigger.

## Adding a migration

1. Write `migrations/000N_short_description.sql`. The number must be unique;
   gaps are fine, duplicates are an error. It does not have to be higher than
   every migration already merged — which migrations have run is recorded in
   `applied_migration`, so one that arrives out of order is still applied.
2. Make the same change to `schema.sql`. A migration that seeds rows goes
   there too, if those rows are part of what a database is expected to hold.
3. Run the tests. They fail if the two disagree.

**Never edit a migration once it has been merged.** A database that already
ran it will not pick the change up, and will silently differ from a fresh one.

`applied_migration` is the runner's own bookkeeping and is not itself a
numbered migration — it is what the numbering is read against. `PRAGMA
user_version` is still maintained, because it is what a `sqlite3` shell shows,
but nothing decides anything from it: as a high-water mark it would skip a
migration numbered below one already applied, which is exactly what two
branches adding migrations produces.

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

**A rebuild is not always the answer.** `ADD COLUMN` does carry a
`REFERENCES` clause, provided the column defaults to NULL — the one case
SQLite allows with foreign keys on — and `DROP COLUMN` copes with a column
that has its own `CHECK`. `0006` replaces `github_repo.review_policy` that
way, and has to: `pr.repo` references `github_repo`, so dropping the old table
would count a deferred violation for every pull request.

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
