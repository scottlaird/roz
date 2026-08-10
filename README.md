# todo

A work queue that stays correct mechanically.

Projects (`SL`) are what you plan from; actions (`NA`) are what you read. An
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
| `todo project list` | The projects table, with `--orphaned` and `--expired`. |
| `todo project set` | Change authored columns. |
| `todo project snooze` | Defer a project to a real date. |
| `todo project wake` | Clear a snooze. |
| `todo project supersede` | Record that one project is the same work as another. |
| `todo project jira` | Record by hand what Jira says. Stands in for Jira sync. |
| **actions** | |
| `todo action add` | Allocate an action and print its id. |
| `todo action show` | Print one action, its blockers and its pull request. |
| `todo action list` | Actions in creation order, with `--open`, `--expired` and filters. |
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
| `todo sync github` | Refresh observed columns from GitHub. Read-only. |
| `todo syncer` | The same on a loop, backing off as rate limit heads down. |
| **vocabulary** | |
| `todo verb list` | The verbs, how each closes, and its rank class. |
| `todo pipeline list` | The pipelines and their steps. |
| **the log** | |
| `todo watch` | Follow the event log, or `--once` to print and exit. |
| `todo note` | Append a note to a subject's history without changing it. |
| `todo exception` | Record an exception for a monitor to surface. |
| **other** | |
| `todo calendar add` / `show` / `list` / `set` | Oncall, PTO and holidays. |
| `todo render` | Regenerate the status page. *Not implemented yet.* |
| `todo verify` | Stamp `last_verified_at`. *Not implemented yet.* |

## A walkthrough

Everything below is real output.

### Set up

```console
$ todo init
initialised /home/scott/.local/share/todo/todo.db (action=NA, project=SL)
```

Prefixes are chosen here and are write-once — identifiers get quoted in
tickets and said out loud, so they cannot be renamed later. Pass
`--project-prefix` and `--action-prefix` if `SL`/`NA` are not what you want.

### Two projects, and a relationship between them

```console
$ todo project add --title "Split the nodepool" --priority 1 --effort weeks --jira-key CDSS-1744
SL1
$ todo project add --title "Retire the old pool" --priority 3 --effort days
SL2
```

It turns out those are the same work:

```console
$ todo project supersede --from SL2 --into SL1
SL2 status: "active" → "superseded"
SL2 superseded_by: "" → "SL1"
```

Superseding records both ends and keeps the identifier. Nothing is deleted,
because `SL2` may already be written down somewhere this tool cannot reach.

```console
$ todo project list
ID   STATUS      PRI  EFFORT  SNOOZED UNTIL  TITLE
SL1  active      1    weeks   -              Split the nodepool
SL2  superseded  3    days    -              Retire the old pool
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
$ todo pr track scottlaird/todo#29
scottlaird/todo#29
```

### Two actions, one waiting on the other

```console
$ todo action add --title "Split the pool config" --verb write --project SL1 \
    --why "everything else waits on it"
NA1
$ todo action add --title "Roll the change out" --verb run --project SL1
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
scottlaird/todo#29 title: "" → "Add a hand-fed Jira sync"
scottlaird/todo#29 author: "" → "scottlaird"
scottlaird/todo#29 url: "" → "https://github.com/scottlaird/todo/pull/29"
scottlaird/todo#29 state: "" → "OPEN"
scottlaird/todo#29 is_draft: "" → "0"
scottlaird/todo#29 in_merge_queue: "" → "0"
scottlaird/todo#29 base_ref: "" → "main"
scottlaird/todo#29 head_sha: "" → "530fa954e1afd45494ebafc14c00e9792f8c7c43"
scottlaird/todo#29 unresolved_threads: "" → "0"
polled 1, 1 changed
```

Read-only, batched into one GraphQL query, and attributed to `sync:github` —
which the store will not let write an authored column. Where GitHub reports
nothing, the stored value is left alone: absence is not a fact.

`todo syncer` runs the same thing on a loop, slowing down as the rate limit
budget drops and backing off on a 429.

### Closing, and the cascade

This is where the queue moves on its own.

```console
$ todo action close NA1 --pr scottlaird/todo#29
NA1 done (completed)
  skipped undraft: already true
  created NA3 send for review scottlaird/todo#29
  created NA4 wait for review scottlaird/todo#29 (blocked by NA3)
  created NA5 merge scottlaird/todo#29 (blocked by NA4)
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

### The steps close themselves — nearly

`send_for_review` closes on the announcement, which is the one signal GitHub
cannot supply. Until Slack sync exists, that is recorded by hand:

```console
$ todo pr announce scottlaird/todo#29 --channel '#infra-reviews'
scottlaird/todo#29 announced_at: "" → "2026-08-10T03:59:17.509Z"
scottlaird/todo#29 announced_channel: "" → "#infra-reviews"
```

Jira is the same arrangement, keyed on the issue rather than the project,
because an integration would have `CDSS-1744` and not `SL1`:

```console
$ todo project jira CDSS-1744 --status "In Progress" --sprint "Sprint 42" --assignee scott
SL1 jira_status: "" → "In Progress"
SL1 jira_sprint: "" → "Sprint 42"
SL1 jira_assignee: "" → "scott"
SL1 jira_synced_at: "" → "2026-08-10T03:59:17.522Z"
```

`--feed` takes a JSON array of the same thing, so faking a whole sync run is
one command. Both are logged as `sync:slack-manual` and `sync:jira-manual`, so
the log never claims an integration reported something typed in by hand.

Closing a step frees the next one through exactly the same unblocking any
action gets — there is no separate notion of advancing a pipeline:

```console
$ todo action close NA3
NA3 done (completed)
  NA4 is now ready

$ todo action list --open
ID   STATE    VERB         PROJECT  SNOOZED UNTIL  TITLE
NA2  ready    run          SL1      -              Roll the change out
NA4  ready    wait_review  SL1      -              wait for review scottlaird/todo#29
NA5  blocked  merge        SL1      -              merge scottlaird/todo#29
```

`NA4` and `NA5` close on their own predicates — an approval and a merge — once
sync observes them. That last step is not wired up yet; see `TODO.md`.

### The log

```console
$ todo watch --once -n 5
2026-08-10T03:59:17.522Z  info  sync:jira-manual   changed  SL1  jira_synced_at: "" → "2026-08-10T03:59:17.522Z"
2026-08-10T03:59:17.535Z  info  human              changed  NA3  state: "ready" → "done"
2026-08-10T03:59:17.535Z  info  human              changed  NA3  closed_at: "" → "2026-08-10T03:59:17.535Z"
2026-08-10T03:59:17.535Z  info  human              changed  NA3  closed_reason: "" → "completed"
2026-08-10T03:59:17.535Z  info  human              changed  NA4  state: "blocked" → "ready"
```

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
