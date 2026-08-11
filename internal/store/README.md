# internal/store

Entities, and the machinery that writes them to the database and to the log.

```
store.go     opening, initialising, DSN pragmas
path.go      where the database lives, per platform
migrate.go   the migration runner
field.go     struct-tag metadata: which column, who may write it, how it encodes
record.go    the Record interface and the field-level diff
relation.go  what an entity is connected to beyond its own columns
actor.go     who is making a change, and what that entitles them to write
tx.go        the unit of work: Load, Insert, Update
event.go     the log: writing it, and reading it back
json.go      JSON in and out, driven by the same field metadata
alloc.go     identifier allocation
sequence.go  identifier prefixes and the entity registry
subject.go   resolving an identifier to whatever it names
rank.go      the queue's ordering, and the terms it is built from
predicate.go how a verb decides its work is done
verb.go      the vocabulary, and the check that every predicate resolves
pipeline.go  what closing a verb instantiates
edge.go      blocking, hiding and pull request links
projectedge.go one project waiting on another, and the cascade
prcheck.go   a row per check, and which transitions are worth an event
close.go     closing an action, and the cascade that follows
settle.go    closing the actions whose predicate has come true
config.go    the settings row, and what counts as a usable one
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

## Field formats

The `format` tag says what a TEXT column holds. Neither format changes
storage: both columns are text in the database and text in the event log.

`format:"json"` marks a column holding JSON. It changes only how the value
crosses the JSON boundary — a structure rather than a quoted string, in both
directions.

`format:"markdown"` marks a column written as prose rather than as a value:
`action.why`, `project.summary`, the two `snooze_reason`s, `calendar_window.note`.
The store keeps the source the author typed, and `show -o json` returns it
unchanged; only the page renders, through `internal/markdown`. A title is not
prose — it is a name, and an author who writes `the *old* pipeline` there means
the asterisks.

The tag carries one input rule, checked in `Tx.Insert` and on the columns
`Tx.Update` actually changes: **no raw HTML**. Almost any string is valid
Markdown, so that is the only thing worth refusing, and refusing it on the way
in tells the author, where stripping it at render time would leave them
wondering why half a sentence vanished. `<https://example.com>` is an autolink
and passes; a literal tag in backticks passes too.

## Settings

`config` is an entity with one row, so settings get the same machinery as
everything else: a diff-based event log, CHECK constraints, field kinds and
typing. That is the argument for columns over a string→string table, and it
costs a migration per setting.

The row is seeded by the migration rather than by `Init`, so no reader has to
decide what an absent configuration means. `Store.Config` reads it afresh each
call rather than caching on the `Store`, because `roz serve` outlives a
`roz config set` in another terminal.

Bespoke validation — that a Jira base URL has a scheme, that a prefix is a
shape the linker could match — lives in `Tx.SaveConfig`, which is the only
path to the row. Same argument as the field kinds: a rule stated at the
database cannot be forgotten at a call site, and the CLI and the MCP server
both arrive here.

## Relations

A `db` tag declares a column. A relation declares a connection that is not one
— what lives in a join table, or the far side of a foreign key pointing back.

```go
func (a *Action) relations() []Relation {
    return []Relation{
        {Name: "blocked_by", Load: blockedByIDs},
        {Name: "blocking", Load: blockingIDs},
        {Name: "subject_pr", Load: subjectPRID},
        {Name: "context_prs", Load: contextPRIDs},
    }
}
```

The declaration names the connection and the function that reads it, and
deliberately **does not describe the join**. The queries stay hand-written and
tested where they are; generating them was never the point. What it buys is
that a reader of the entity can see what it is attached to, and that
`Tx.MarshalRecord` gives `show` and `show -o json` the same answer — the table
used to print an action's blockers while the JSON silently omitted them.

A connection that is already a column needs nothing here. `action.project_id`
and `action.hidden_behind` are declared by their tags like anything else,
which is why the lists are short.

A relation with nothing at the far end is **left out**, not rendered empty, so
absent and none read the same.

**One query per relation**, which is right for one record and wrong for a
list. The status page keeps its own batched loaders — `PRsByAction`,
`JiraByProject` — and reads them for every row it draws. Unifying those too
would mean a batch loader in every declaration, which is worth doing when a
third consumer appears and not before.

## Adding an entity

1. Define the struct with `db` and `kind` tags, and `table()`,
   `subjectType()` and `subjectID()` methods.
2. Add a constructor. For a sequence-numbered entity, allocation is
   **separate** — build and validate the record first, then call
   `Store.Allocate…` last, so rejected input does not consume a number.
3. Add `Tx.Load<Entity>` and a `Clone`.
4. Add a list query if it needs one. Order by `n`, not `id`: `ROZ100` sorts
   before `ROZ41` as text, which is the whole reason `n` exists.
5. Teach `subject.go` to resolve its identifiers, if `note` and `exception`
   should work against it.
6. Declare its relations, if it is connected to anything that is not a column
   of its own table.

Nothing else needs touching. JSON, diffing and event emission follow from the
tags.

## Rules that are easy to break

**Allocation commits on its own.** `Store.Allocate` deliberately does not take
a `Tx`, so a failed insert still consumes its number. Gaps are fine; reuse is
not, because an identifier may already have been written into a ticket or said
out loud. Never derive the next number from `max(n)`.

**Identifier prefixes live in the database**, seeded at init and write-once.
There is no hardcoded `ROZ` anywhere outside the `roz init` flag defaults.
`EntityForID` resolves a prefix through that registry.

**`subject_id` in the log is heterogeneous by design.** It holds `ROZ200`,
`scottlaird/roz#11` or `scottlaird/roz`. Nothing joins on it, so nothing
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
