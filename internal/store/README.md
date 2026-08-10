# internal/store

Entities, and the machinery that writes them to the database and to the log.

```
store.go     opening, initialising, DSN pragmas
path.go      where the database lives, per platform
migrate.go   the migration runner
field.go     struct-tag metadata: which column, who may write it, how it encodes
record.go    the Record interface and the field-level diff
actor.go     who is making a change, and what that entitles them to write
tx.go        the unit of work: Load, Insert, Update
event.go     the log: writing it, and reading it back
json.go      JSON in and out, driven by the same field metadata
alloc.go     identifier allocation
sequence.go  identifier prefixes and the entity registry
subject.go   resolving an identifier to whatever it names
predicate.go how a verb decides its work is done
verb.go      the vocabulary, and the check that every predicate resolves
pipeline.go  what closing a verb instantiates
edge.go      blocking, hiding and pull request links
close.go     closing an action, and the cascade that follows
settle.go    closing the actions whose predicate has come true
project.go  action.go  pr.go  repo.go  calendar.go   the entities
```

## The one idea

An entity is a struct whose fields carry `db` tags. Everything else reads
those tags: the diff, the `UPDATE` builder, the `SELECT` column list, the JSON
encoder and decoder. Adding an entity is a struct plus tags, not a set of
parallel implementations that have to be kept in step.

```go
type Project struct {
    ID     string         `db:"id" kind:"identity"`
    Title  string         `db:"title"`
    Status string         `db:"status"`
    JiraStatus sql.NullString `db:"jira_status" kind:"observed"`
    DesignRefs string     `db:"design_refs" format:"json"`
    CreatedAt  string     `db:"created_at" kind:"created"`
    UpdatedAt  string     `db:"updated_at" kind:"auto"`
}
```

Events are not written by hand. `Tx.Update` diffs the before and after images
and writes one event row per column that actually moved, so a no-op update
writes nothing at all.

```go
tx, _ := st.Begin(ctx, store.ActorHuman)   // one correlation id per command
after := before.Clone()
after.Title = "new title"
changes, _ := tx.Update(ctx, before, after)
tx.Commit()
```

## Field kinds

The `kind` tag says who may write a column. It is the design sketch's
authored/observed split made enforceable in one place rather than remembered
at every call site.

| kind | who writes it | logged |
|---|---|---|
| `authored` (the default) | a human or an agent | yes |
| `observed` | `sync:*` actors only | yes |
| `derived` | nobody; the database computes it | no |
| `identity` | the caller, at insert; immutable after | changing it is an error |
| `created` | the store, at insert; immutable after | changing it is an error |
| `auto` | the store, on every write | no |

`Tx.Update` rejects a write the actor is not entitled to make, and
`Tx.Insert` applies the same rule to a new record — it has no before image, so
the test is whether a forbidden column was given a value at all. An empty
value is not a claim, and neither is an empty JSON container.

`format:"json"` marks a TEXT column holding JSON. It changes only how the
value crosses the JSON boundary — a structure rather than a quoted string, in
both directions. In the database and in the event log it stays text.

## Adding an entity

1. Define the struct with `db` and `kind` tags, and `table()`,
   `subjectType()` and `subjectID()` methods.
2. Add a constructor. For a sequence-numbered entity, allocation is
   **separate** — build and validate the record first, then call
   `Store.Allocate…` last, so rejected input does not consume a number.
3. Add `Tx.Load<Entity>` and a `Clone`.
4. Add a list query if it needs one. Order by `n`, not `id`: `SL100` sorts
   before `SL41` as text, which is the whole reason `n` exists.
5. Teach `subject.go` to resolve its identifiers, if `note` and `exception`
   should work against it.

Nothing else needs touching. JSON, diffing and event emission follow from the
tags.

## Rules that are easy to break

**Allocation commits on its own.** `Store.Allocate` deliberately does not take
a `Tx`, so a failed insert still consumes its number. Gaps are fine; reuse is
not, because an identifier may already have been written into a ticket or said
out loud. Never derive the next number from `max(n)`.

**Identifier prefixes live in the database**, seeded at init and write-once.
There is no hardcoded `SL` anywhere outside the `todo init` flag defaults.
`EntityForID` resolves a prefix through that registry.

**`subject_id` in the log is heterogeneous by design.** It holds `SL200`,
`scottlaird/todo#11` or `scottlaird/todo`. Nothing joins on it, so nothing
cares — but a parser must reject what is not its own shape rather than guess.

**The log is append-only and has no foreign keys.** Deleting an entity leaves
its history intact. Events are written in the same transaction as the change,
so a rollback leaves no trace of something that did not happen.

**NULL and the empty string are indistinguishable in the log**, because
`event.old_value` and `event.new_value` are `TEXT NOT NULL`. That is the
sketch's choice, and it keeps every log query a plain string comparison. JSON
output is not constrained that way and renders NULL as `null`.

## Testing

`newStore(t)` gives a `Store` over a fresh initialised database with the clock
advanced a second per transaction, so timestamps are predictable but two units
of work stay distinguishable. Diff and encoding rules are tested against a
synthetic record in `record_test.go`, so they do not depend on the real schema.
