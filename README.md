# todo

A work queue that stays correct mechanically.

Projects (`TD`) are what you plan from; actions (`NA`) are what you read. An
action's verb decides how it closes — a *predicate* verb closes when GitHub
says the work is done, a *human* verb only when you say so — so the only items
that reach the queue as thinking work are the ones that need thinking about.
Everything else flows through on its own.

Every change is an event, and events come from diffing records rather than
from hand-written log calls, so the history cannot drift from the data. Sync
may write only *observed* columns and people only *authored* ones: a sync can
never overwrite a judgement, and a judgement can never invent a fact.

Go, SQLite (`modernc.org/sqlite`, no cgo), and `gh` for GitHub reads.

```
go install github.com/scottlaird/todo@latest
```

## Commands

The database is `--db`, or `$TODO_DB`, defaulting to a per-platform user data
directory.

| Command | What it does |
|---|---|
| `todo init` | Create the database, apply the schema, and fix the identifier prefixes. Safe to re-run; it also migrates. |
| **projects** | |
| `todo project add` | Allocate a project and print its id. |
| `todo project show` | Print one project in full. |
| `todo project list` | The projects table, in creation order or `--sort priority`; with `--orphaned` and `--expired`. |
| `todo project set` | Change authored columns. |
| `todo project snooze` | Defer a project to a real date. |
| `todo project wake` | Clear a snooze. |
| `todo project supersede` | Record that one project is the same work as another. |
| `todo project close` | Close it, dropping whatever was still open on it. |
| `todo project jira` | Record by hand what Jira says. Stands in for Jira sync. |
| **actions** | |
| `todo action add` | Allocate an action and print its id. |
| `todo action show` | Print one action, its blockers and its pull request. |
| `todo action list` | Actions in creation order, or `--sort priority`; with `--unblocked`, `--open`, `--expired` and filters. |
| `todo action set` | Change authored columns. Closing is not one of them. |
| `todo action snooze` | Defer an action to a real date. |
| `todo action wake` | Clear a snooze. |
| `todo action add-blocker` | Record that one action must precede another. |
| `todo action hide-behind` | Fold an action out of the queue until another clears. |
| `todo action link-pr` | Attach a pull request as subject or context. |
| `todo action close` | Close it, and run the cascade. |
| **GitHub** | |
| `todo repo track` | Start tracking a repository and choose its pipeline. |
| `todo repo show` / `list` / `set` | Read and change repository policy. |
| `todo pr track` | Start tracking a pull request, keyed `owner/repo#number`. |
| `todo pr show` / `list` | Read tracked pull requests. |
| `todo pr announce` | Record by hand that it was announced in Slack. Stands in for Slack sync. |
| `todo sync github` | Refresh observed columns from GitHub, and close the steps GitHub has finished. Read-only against GitHub. |
| `todo syncer` | The same on a loop, backing off as rate limit heads down. |
| `todo serve` | Sync, tail the log and serve the page together, until interrupted. The page reloads itself when the log moves. Loopback, no authentication. |
| **vocabulary** | |
| `todo verb list` | The verbs, how each closes, and its rank class. |
| `todo pipeline list` | The pipelines and their steps. |
| **the log** | |
| `todo watch` | Follow the event log, or `--once` to print and exit. |
| `todo note` | Append a note to a subject's history without changing it. |
| `todo exception` | Record an exception for a monitor to surface. |
| **other** | |
| `todo calendar add` / `show` / `list` / `set` | Oncall, PTO and holidays. |
| `todo render` | Regenerate the status page: three `<pre>` blocks, ordered by priority, no design worth the name. |
| `todo verify` | Stamp `last_verified_at`. *Not implemented yet.* |

## A walkthrough

Everything below is real output, captured by running these commands in order
against a fresh database and the live `scottlaird/todo#39`. That pull request
has moved on since, so re-running it today will answer differently — which is
rather the point of the tool.

### Set up

```console
$ todo init
initialised /home/scott/.local/share/todo/todo.db (action=NA, project=TD)
```

Prefixes are chosen here and are write-once — identifiers get quoted in
tickets and said out loud, so they cannot be renamed later. Pass
`--project-prefix` and `--action-prefix` if `TD`/`NA` are not what you want.

### Two projects, and a relationship between them

```console
$ todo project add --title "Split the nodepool" --priority 1 --effort weeks --jira-key CDSS-1744
TD1
$ todo project add --title "Retire the old pool" --priority 3 --effort days
TD2
```

It turns out those are the same work:

```console
$ todo project supersede --from TD2 --into TD1
TD2 status: "active" → "superseded"
TD2 superseded_by: "" → "TD1"
```

Superseding records both ends and keeps the identifier. Nothing is deleted,
because `TD2` may already be written down somewhere this tool cannot reach.

```console
$ todo project list
ID   STATUS      PRI  EFFORT  SNOOZED UNTIL  TITLE
TD1  active      1    weeks   -              Split the nodepool
TD2  superseded  3    days    -              Retire the old pool
```

### A repository and a pull request

```console
$ todo repo track scottlaird/todo --announce-channel '#infra-reviews'
scottlaird/todo
$ todo repo list
ID               PIPELINE  DEFAULT BRANCH  ANNOUNCE        DISPOSITION
scottlaird/todo  review    -               #infra-reviews  -
```

The repository took the `review` pipeline, which is the lowest-numbered active
one. `todo pipeline list` shows the choices; `--pipeline direct` at track time
picks the other.

A repository must be tracked before its pull requests, because the pipeline
decides what a pull request against it will need doing to it.

```console
$ todo pr track scottlaird/todo#39
scottlaird/todo#39
```

### Two actions, one waiting on the other

```console
$ todo action add --title "Split the pool config" --verb write --project TD1 \
    --why "everything else waits on it"
NA1
$ todo action add --title "Roll the change out" --verb run --project TD1
NA2
$ todo action add-blocker --from NA2 --to NA1
NA2 is blocked, waiting on NA1
```

`--verb` comes from the vocabulary (`todo verb list`) and is load-bearing
rather than descriptive. `write` and `run` are human-closed, so both sit in
the queue until you say otherwise.

### Sync

```console
$ todo sync github
scottlaird/todo#39 title: "" → "Default new databases to the TD project prefix"
scottlaird/todo#39 author: "" → "scottlaird"
scottlaird/todo#39 url: "" → "https://github.com/scottlaird/todo/pull/39"
scottlaird/todo#39 state: "" → "OPEN"
scottlaird/todo#39 is_draft: "" → "0"
scottlaird/todo#39 merge_state_status: "" → "CLEAN"
scottlaird/todo#39 in_merge_queue: "" → "0"
scottlaird/todo#39 base_ref: "" → "main"
scottlaird/todo#39 head_sha: "" → "6d7450f3ce139e323fa2264191598ff0d1418f1a"
scottlaird/todo#39 unresolved_threads: "" → "0"
polled 1, 1 changed
```

Read-only, batched into one GraphQL query, and attributed to `sync:github` —
which the store will not let write an authored column. Where GitHub reports
nothing, the stored value is left alone: absence is not a fact.

Sync also closes any action whose predicate the new observations satisfy; the
walkthrough gets to that below.

`todo syncer` runs the same thing on a loop, slowing down as the rate limit
budget drops and backing off on a 429.

### Closing, and the cascade

This is where the queue moves on its own.

```console
$ todo action close NA1 --pr scottlaird/todo#39
NA1 done (completed)
  skipped undraft: already true
  created NA3 send for review scottlaird/todo#39
  created NA4 wait for review scottlaird/todo#39 (blocked by NA3)
  created NA5 merge scottlaird/todo#39 (blocked by NA4)
  NA2 is now ready
```

Four things happened under one correlation id:

- **The pipeline was instantiated.** Closing a `write` action produces the
  chain that follows it, each step blocked by the one before.
- **`undraft` was skipped.** Sync had just observed that the pull request is
  not a draft, so there is nothing to un-draft, and an action that is complete
  before it exists is noise. Had the pull request never been synced, the step
  would have been created — a predicate is false where nothing was observed,
  because absence is not completion.
- **`NA2` was freed**, its last open blocker having closed.
- Anything hidden behind `NA1` would have come back too.

### The steps close themselves

`send_for_review` closes on the announcement, which is the one signal GitHub
cannot supply. Until Slack sync exists, that is recorded by hand:

```console
$ todo pr announce scottlaird/todo#39 --channel '#infra-reviews'
scottlaird/todo#39 announced_at: "" → "2026-08-10T14:47:22.041Z"
scottlaird/todo#39 announced_channel: "" → "#infra-reviews"
```

Jira is the same arrangement, keyed on the issue rather than the project,
because an integration would have `CDSS-1744` and not `TD1`:

```console
$ todo project jira CDSS-1744 --status "In Progress" --sprint "Sprint 42" --assignee scott
TD1 jira_status: "" → "In Progress"
TD1 jira_sprint: "" → "Sprint 42"
TD1 jira_assignee: "" → "scott"
TD1 jira_synced_at: "" → "2026-08-10T14:47:22.053Z"
```

`--feed` takes a JSON array of the same thing, so faking a whole sync run is
one command. Both are logged as `sync:slack-manual` and `sync:jira-manual`, so
the log never claims an integration reported something typed in by hand.

Now the announcement is a fact, `send_for_review` is satisfied, and the next
sync notices:

```console
$ todo sync github
NA3 closed: scottlaird/todo#39 is send_for_review
  NA4 is now ready
polled 1, 0 changed, 1 closed

$ todo action list --open
ID   STATE    VERB         PROJECT  SNOOZED UNTIL  TITLE
NA2  ready    run          TD1      -              Roll the change out
NA4  ready    wait_review  TD1      -              wait for review scottlaird/todo#39
NA5  blocked  merge        TD1      -              merge scottlaird/todo#39
```

Nobody closed `NA3`. A predicate verb says how its action closes, and sync is
what asks. `NA4` and `NA5` go the same way once GitHub reports the approval
and the merge — leave `todo syncer` running and the chain empties itself.

Closing a step frees the next through exactly the same unblocking any action
gets, whether a person closed it or a predicate did. There is no separate
notion of advancing a pipeline.

Two rules keep this from being alarming. A predicate is false wherever nothing
has been observed, so an unreachable GitHub cannot empty the queue — absence
is not completion. And settling is written as `predicate`, not as
`sync:github`: observing that a pull request merged and deciding the merge
action is done are different acts, and the log keeps them apart.

### The log

```console
$ todo watch --once -n 5
2026-08-10T14:47:22.053Z  info  sync:jira-manual  changed  TD1  jira_synced_at: "" → "2026-08-10T14:47:22.053Z"
2026-08-10T14:47:22.567Z  info  predicate         changed  NA3  state: "ready" → "done"
2026-08-10T14:47:22.567Z  info  predicate         changed  NA3  closed_at: "" → "2026-08-10T14:47:22.567Z"
2026-08-10T14:47:22.567Z  info  predicate         changed  NA3  closed_reason: "" → "completed"
2026-08-10T14:47:22.567Z  info  predicate         changed  NA4  state: "blocked" → "ready"
```

Note the actor: `predicate`, not `human` and not `sync:slack-manual`. One act
recorded that the pull request was announced; a second decided the action was
therefore done. Those are different claims, and the log keeps them apart.

Without `--once` it follows. Every row came from a diff between two versions
of a record — nothing writes to the log by hand except the edge and lifecycle
events, which have no column to diff.

## Where to read more

- [`TODO.md`](TODO.md) — what is left, what is settled, and what is
  deliberately out of scope.
- [`internal/schema/README.md`](internal/schema/README.md) — the schema, the
  migration rules, and what SQLite will not let you do.
- [`internal/store/README.md`](internal/store/README.md) — entities, field
  kinds, and how to add one.
