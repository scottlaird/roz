# roz

<img src="assets/roz-256.png" alt="" width="128" align="right">

A work queue that stays correct mechanically.

Projects (`ROZ`) are what you plan from; actions (`NA`) are what you read. An
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
go install github.com/scottlaird/roz@latest
```

## Commands

The database is `--db`, or `$ROZ_DB`, defaulting to a per-platform user data
directory.

| Command | What it does |
|---|---|
| `roz init` | Create the database, apply the schema, and fix the identifier prefixes. Safe to re-run; it also migrates. |
| `roz config show` / `set` | The settings the database carries: the Jira host, the project keys worth linking, and whose queue this is. |
| **projects** | |
| `roz project add` | Allocate a project and print its id. |
| `roz project show` | Print one project in full. |
| `roz project list` | The projects table, in creation order or `--sort priority`; with `--orphaned` and `--expired`. |
| `roz project set` | Change authored columns. |
| `roz project snooze` | Defer a project to a real date. |
| `roz project wake` | Clear a snooze. |
| `roz project supersede` | Record that one project is the same work as another. |
| `roz project close` | Close it, dropping whatever was still open on it and freeing whatever waited on it. |
| `roz project block` / `unblock` | Record that one project must finish before another can start, or that it need not. |
| `roz project link-issue` / `unlink-issue` | Say which tracker issues a project tracks. More than one is allowed, from more than one tracker. |
| **tracker issues** | |
| `roz issue show` / `list` | Issues as last observed, and which projects track them. |
| `roz issue observe` | Record by hand what a tracker says about an issue — summary, status, iteration, assignee. GitHub issues are read by `roz sync`; for any other tracker this is how they get in. |
| **actions** | |
| `roz action add` | Allocate an action and print its id. `--pr` names the pull request it is about, which a predicate verb requires. |
| `roz action show` | Print one action, with what blocks it, what it blocks, and its pull requests. `-o json` carries the same. |
| `roz action list` | Actions in creation order, or `--sort priority`; with `--unblocked`, `--waiting`, `--stale`, `--open`, `--expired` and filters. |
| `roz action set` | Change authored columns. Closing is not one of them. |
| `roz action snooze` | Defer an action to a real date. |
| `roz action wake` | Clear a snooze. |
| `roz action add-blocker` | Record that one action must precede another. |
| `roz action hide-behind` | Fold an action out of the queue until another clears. |
| `roz action link-pr` | Attach a pull request as subject or context. |
| `roz action close` | Close it, and run the cascade. |
| **GitHub** | |
| `roz repo track` | Start tracking a repository and choose its pipeline. |
| `roz repo show` / `list` / `set` | Read and change repository policy. |
| `roz pr track` | Start tracking a pull request, keyed `owner/repo#number`. `--pipeline` if this one reaches merge differently from its repository. |
| `roz pr set` | Change the pipeline or the tracking reason, or clear either. The two authored columns a pull request has. |
| `roz pr show` / `list` | Read tracked pull requests; `--because reviewing` for what you owe a review on. |
| `roz pr announce` | Record by hand that it was announced in Slack. Stands in for Slack sync, and closes the `send_for_review` step waiting on it. |
| `roz sync github` | Refresh observed columns from GitHub, and close the steps GitHub has finished. Read-only against GitHub. |
| `roz syncer` | The same on a loop, backing off as rate limit heads down. |
| `roz mcp` | Serve the commands over MCP on stdio, for an agent. Writes are recorded as `agent:<client>`. |
| `roz serve` | Sync, tail the log and serve the page together, until interrupted. The page reloads itself when the log moves. Loopback, no authentication. |
| **vocabulary** | |
| `roz codeowners` | Who has to approve a set of changed files, and who is still worth asking. |
| `roz verb set` | Change a verb's `wait_days` or `rank_class`. Settings, not definitions. |
| `roz verb list` | The verbs, how each closes, its rank class, and how long waiting on one is reasonable. |
| `roz pipeline add` / `set` / `retire` | Define a chain, change its steps, take it out of use for new repositories. |
| `roz pipeline show` / `list` | One pipeline and what uses it, or all of them. |
| **the log** | |
| `roz watch` | Follow the event log, or `--once` to print and exit. |
| `roz note` | Append a note to a subject's history without changing it. |
| `roz exception` | Record an exception for a monitor to surface. |
| **other** | |
| `roz calendar add` / `show` / `list` / `set` | Oncall, PTO and holidays. |
| `roz page set` / `show` / `list` / `clear` | Prose the page places, keyed by slot. |
| `roz render` | Regenerate the status page: calendar, queue, what is merely waiting, and the projects table. Prose fields render as Markdown, and GitHub and Jira identifiers become links wherever they are written; Jira needs `roz config set`. |
| `roz verify` | Record that a project or action was checked against reality. Feeds `--sort staleness`. |
| `roz db backup` / `restore` | Copy the database out with `VACUUM INTO`, and put one back. |

## A walkthrough

Everything below is real output, captured by running these commands in order
against a fresh database and the live `scottlaird/roz#39`. That pull request
has moved on since, so re-running it today will answer differently — which is
rather the point of the tool.

### Set up

```console
$ roz init
initialised /home/scott/.local/share/roz/roz.db (schema 15, action=NA, project=ROZ)
```

Prefixes are chosen here and are write-once — identifiers get quoted in
tickets and said out loud, so they cannot be renamed later. Pass
`--project-prefix` and `--action-prefix` if `ROZ`/`NA` are not what you want.

`init` is also the upgrade path, and is safe to re-run. It says which of the
three things happened, so a schema that moved is visible rather than folded
into a note about the database already existing:

```console
$ roz init
migrated /home/scott/.local/share/roz/roz.db: schema 14 → 15 (action=NA, project=ROZ)

$ roz init
/home/scott/.local/share/roz/roz.db is up to date (schema 15, action=NA, project=ROZ)
```

```console
$ roz config set --owner scott --jira-base-url https://example.atlassian.net/browse --jira-prefix CDSS
config owner: "" → "scott"
config jira_base_url: "" → "https://example.atlassian.net/browse"
config jira_prefixes: "[]" → "[\"CDSS\"]"
```

These are properties of the queue rather than of one command, so they live in
the database. See [Settings](#settings).

### Two projects, and a relationship between them

```console
$ roz project add --title "Split the nodepool" --priority 1 --effort weeks --issue CDSS-1744
ROZ1
$ roz project add --title "Retire the old pool" --priority 3 --effort days
ROZ2
```

It turns out those are the same work:

```console
$ roz project supersede --from ROZ2 --into ROZ1
ROZ2 status: "active" → "superseded"
ROZ2 superseded_by: "" → "ROZ1"
```

Superseding records both ends and keeps the identifier. Nothing is deleted,
because `ROZ2` may already be written down somewhere this tool cannot reach.

```console
$ roz project list
ID    STATUS      PRI  EFFORT  SNOOZED UNTIL  TITLE
ROZ1  active      1    weeks   -              Split the nodepool
ROZ2  superseded  3    days    -              Retire the old pool
```

### A repository and a pull request

```console
$ roz repo track scottlaird/roz --announce-channel '#infra-reviews'
scottlaird/roz
$ roz repo list
ID              PIPELINE  DEFAULT BRANCH  ANNOUNCE        DISPOSITION
scottlaird/roz  review    -               #infra-reviews  -
```

The repository took the `review` pipeline, which is the lowest-numbered active
one. `roz pipeline list` shows the choices; `--pipeline direct` at track time
picks the other.

A repository must be tracked before its pull requests, because the pipeline
decides what a pull request against it will need doing to it.

```console
$ roz pr track scottlaird/roz#39
scottlaird/roz#39
```

### Two actions, one waiting on the other

```console
$ roz action add --title "Split the pool config" --verb write --project ROZ1 \
    --why "everything else waits on it"
NA1
$ roz action add --title "Roll the change out" --verb run --project ROZ1
NA2
$ roz action add-blocker --from NA2 --to NA1
NA2 is blocked, waiting on NA1
```

`--verb` comes from the vocabulary (`roz verb list`) and is load-bearing
rather than descriptive. `write` and `run` are human-closed, so both sit in
the queue until you say otherwise.

A predicate verb is refused without a pull request to ask, because the answer
would be false forever and the action could never close:

```console
$ roz action add --title "bring it up to date" --verb rebase
Error: "rebase" closes when pr_mergeable says so, so it needs a pull request to ask: pass --pr, or use a verb that closes on a person and let closing it open the chain
```

`review` is unaffected: it carries a pull request and still closes on a
person, so the rule is about how a verb closes, not about whether it has one.

Supplying the subject is also the moment the predicate becomes answerable, so
it is asked there and then rather than at the next poll:

```console
$ roz action add --title "merge it" --verb merge --pr owner/repo#1
NA2
NA2 closed: owner/repo#1 is merge
```

`action link-pr --role subject` and `action set --verb` do the same, for the
same reason. Only the action named is settled, plus whatever its closing
frees — everything else on that pull request is sync's job.

### Sync

```console
$ roz sync github
scottlaird/roz#39 title: "" → "Default new databases to the TD project prefix"
scottlaird/roz#39 author: "" → "scottlaird"
scottlaird/roz#39 url: "" → "https://github.com/scottlaird/roz/pull/39"
scottlaird/roz#39 state: "" → "OPEN"
scottlaird/roz#39 is_draft: "" → "0"
scottlaird/roz#39 merge_state_status: "" → "CLEAN"
scottlaird/roz#39 in_merge_queue: "" → "0"
scottlaird/roz#39 base_ref: "" → "main"
scottlaird/roz#39 head_sha: "" → "6d7450f3ce139e323fa2264191598ff0d1418f1a"
scottlaird/roz#39 unresolved_threads: "" → "0"
polled 1, 1 changed
```

Read-only, batched into one GraphQL query, and attributed to `sync:github` —
which the store will not let write an authored column. Where GitHub reports
nothing, the stored value is left alone: absence is not a fact.

**It asks about what can still move**, rather than everything ever tracked.
Nothing untracks a row, so the tracked set only grows, and the observed
columns of a pull request that merged last quarter cannot change again. Three
things are polled:

- Anything whose **stored** state is open. Stored, never what GitHub last
  said — the transition into `MERGED` is itself an observation, so a row that
  is locally open stays in the set however old it is. The other way round, a
  pull request that merges is never seen to have merged.
- Anything that ended within `poll_window_days`, default 14. An ending is not
  the last thing that happens: review comments and thread resolutions land
  after a merge, and `human_commented_at` is what the amend-versus-force-push
  rule reads. The window is generous rather than tight, because a wide one
  costs a few entities per poll and a narrow one costs an observation nobody
  makes.
- Anything an **open action** is about, whatever its age. Tracking something
  long merged and then writing an action about it is ordinary use, and without
  this the predicate never gets an observation and the action cannot close.

```bash
roz config set --poll-window-days 30
```

Nothing is deleted and no filter changes. This is about what is *asked*, which
is a different question from what is kept: `pr list --state MERGED` and
`--since` answer from the store exactly as they did.

The cost is real and worth stating: **a closed pull request that is reopened
outside the window is not noticed**, and no later poll recovers it. Merging
carries no such hazard, and reopening after a fortnight is rare enough to be
worth the trade — but it is a trade rather than a free win. Widen the window,
or write an action against the pull request, if it matters for one.

Sync also closes any action whose predicate the new observations satisfy, and
reads the GitHub issues projects track; the walkthrough gets to both below.

`roz syncer` runs the same thing on a loop, slowing down as the rate limit
budget drops and backing off on a 429.

**A cycle makes several reads, and one failing no longer cancels the others.**
Refs, issues and pull requests are read in that order — refs first because a
release gate has to have its ref recorded before anything asks whether it
arrived — and before this, the first read to fail took the rest of the cycle
with it. Pull requests are last, so a ref read that failed every cycle meant
they were never polled at all.

Now what succeeds is applied, and what failed is said:

```console
sync read refs failed, carrying on with the rest: gh: connection reset
```

**A rate limit still stops the cycle where it is.** The reads share one
GraphQL budget, so carrying on after one has been refused spends against a
limit already hit. Attempting all of them is the rule for faults; a limit is
not a fault.

**A partly-read cycle does not count towards giving up.** It is a working
cycle, and counting it would eventually stop a process doing most of its job —
taking the services sharing it down too. What that trades away is the
guarantee that a persistent failure ends in something louder than a log line,
so the line is written every cycle rather than once, and names the read. A
cycle that got nothing at all from GitHub is a failed cycle and still counts.

Settling on a partly-read cycle is safe, and it is worth knowing why: an
observation that did not happen records nothing, so a predicate simply does
not fire. Absence is not a negative observation. Nothing "completes" a partial
cycle by clearing what it did not read — that would close or reopen things on
no evidence.

### Closing, and the cascade

This is where the queue moves on its own.

```console
$ roz action close NA1 --pr scottlaird/roz#39
NA1 done (completed)
  skipped undraft: already true
  created NA3 send for review scottlaird/roz#39
  created NA4 wait for review scottlaird/roz#39 (blocked by NA3)
  created NA5 merge scottlaird/roz#39 (blocked by NA4)
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
$ roz pr announce scottlaird/roz#39 --channel '#infra-reviews'
scottlaird/roz#39 announced_at: "" → "2026-08-10T14:47:22.041Z"
scottlaird/roz#39 announced_channel: "" → "#infra-reviews"
NA3 closed: scottlaird/roz#39 is send_for_review
  NA4 is now ready
```

The last two lines are the point. The fact that closes the step has just
arrived, so the step closes now rather than at the next poll — the same settle
pass `roz sync` runs. It means a command that reads as "write down what I did
in Slack" also closes actions and instantiates what follows them, which is why
it prints what it closed.

Tracker issues are the same arrangement, keyed on the issue rather than the
project, because an integration would have `CDSS-1744` and not `ROZ1` — and
because an issue is a record in its own right, so one nothing tracks is still
stored:

```console
$ roz issue observe CDSS-1744 --status "In Progress" --iteration "Sprint 42" --assignee scott
jira:CDSS-1744 status: "" → "In Progress"
jira:CDSS-1744 iteration: "" → "Sprint 42"
jira:CDSS-1744 assignee: "" → "scott"
jira:CDSS-1744 synced_at: "" → "2026-08-12T00:56:43.074Z"
```

`--feed` takes a JSON array of the same thing, so faking a whole sync run is
one command. Both are logged as `sync:slack-manual` and `sync:jira-manual`, so
the log never claims an integration reported something typed in by hand.

An issue is identified by its tracker and that tracker's own key, written
together: `jira:CDSS-1744`, `github:owner/repo#123`. `--tracker` defaults to
`jira`, which has no integration and so is observed by hand; GitHub issues are
read by `roz sync`, below:

```console
$ roz project link-issue --project ROZ1 --tracker github --issue scottlaird/roz#101
ROZ1 tracks github:scottlaird/roz#101

$ roz issue list
ID                         STATUS       ITERATION  ASSIGNEE  SUMMARY
github:scottlaird/roz#101  -            -          -         -
jira:CDSS-1744             In Progress  Sprint 42  scott     Allow scaling up
```

The id is composed rather than trusting two third parties' key formats never
to collide. `iteration` is Jira's sprint and GitHub's milestone: the same
field under two names, so it carries neither.

A GitHub issue does not stay blank, because sync reads it:

```console
$ roz sync github
scottlaird/roz#101 summary: "" → "Generalise the jira_* columns to a tracker and a key"
scottlaird/roz#101 status: "" → "OPEN"
polled 1, 0 changed, 1 issue polled
```

Every tracked issue is read on every poll, batched into the same kind of
aliased GraphQL query the pull requests use, and written through the same path
`roz issue observe` writes through — a poll and a person typing are the same
kind of act, an observation about somebody else's tracker, and the only thing
that differs is the actor recorded against it. Reading one again says nothing
at all: a reading that found no change writes nothing and logs nothing, so a
syncer polling every fifteen seconds stays quiet until something moves.

That makes `synced_at` mean *when the stored state last changed*, which is the
same trade `pr.last_synced_at` makes and for the same reason — the alternative
is a row written and an event logged per issue per cycle, burying every real
transition under a heartbeat. A caller that states a time keeps the older
meaning: `roz issue observe --at` records when somebody looked, because that
is a fact being asserted rather than a poll finding nothing.

The status is GitHub's own word. `OPEN` and `CLOSED` are not translated into a
vocabulary roz prefers, because the column holds what a tracker said and no
tracker agrees with another about what its states are called.

One thing is worth interrupting for. An issue that closes with actions still
open against it means either the queue is stale or the ticket went early:

```console
$ roz sync github
scottlaird/roz#105 status: "OPEN" → "CLOSED"
scottlaird/roz#105 closed with NA1 still open
polled 0, 0 changed, 3 issues polled
```

```console
$ roz watch --once -n 1
2026-08-13T03:44:02.080Z  exception  sync:github  issue_closed_with_open_actions  github:scottlaird/roz#105  scottlaird/roz#105 closed with NA1 still open
```

An exception rather than an action, because which of the two it should be
depends on the project: whether an issue closing means the work is finished or
means somebody was optimistic is not something roz can know.

It fires on the transition, so an issue that stays closed does not say so
again, and an issue that was already closed the first time roz read it says
nothing at all — there is no telling whether that happened this morning or two
years ago, and a project linked to a long-closed issue is an ordinary way to
record where the work came from. An issue that GitHub will not resolve is
reported the same way, once a day; the usual cause is a key naming a pull
request, since GitHub numbers both from one sequence.

Now the announcement is a fact, `send_for_review` is satisfied, and the next
sync notices:

```console
$ roz sync github
NA3 closed: scottlaird/roz#39 is send_for_review
  NA4 is now ready
polled 1, 0 changed, 1 closed, 1 issue polled

$ roz action list --open
ID   STATE    VERB         PROJECT  SNOOZED UNTIL  TITLE
NA2  ready    run          ROZ1     -              Roll the change out
NA4  ready    wait_review  ROZ1     -              wait for review scottlaird/roz#39
NA5  blocked  merge        ROZ1     -              merge scottlaird/roz#39
```

Nobody closed `NA3`. A predicate verb says how its action closes, and sync is
what asks. `NA4` and `NA5` go the same way once GitHub reports the approval
and the merge — leave `roz syncer` running and the chain empties itself.

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
$ roz watch --once -n 5
2026-08-10T14:47:22.053Z  info  sync:jira-manual  changed  CDSS-1744  synced_at: "" → "2026-08-10T14:47:22.053Z"
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

## Settings

Three things are properties of the queue rather than of one command: the Jira
host, the project keys worth linking, and whose queue it is. They were flags,
which meant passing them on every invocation — and meant anything reading the
database directly could not know them at all.

```console
$ roz config show
created_at     2026-08-11T02:09:46.064Z
id             config
jira_base_url  https://example.atlassian.net/browse
jira_prefixes  ["CDSS"]
owner          scott
updated_at     2026-08-11T02:09:46.088Z
```

They are **columns, not a key-value bag**, which is the same reasoning the
rest of the schema follows. A settings change then gets the diff-based event
log for free — `roz watch` shows the page starting to link somewhere new —
plus CHECK constraints and real types, where a string→string table gets none
of that and invites `enable_foo = "true"`. The cost is a migration per
setting, which for a handful of settings is the right trade.

`--jira-prefix` replaces the whole list rather than adding to it, so there is
a way to remove one; pass it once per key, or `--jira-prefix ""` to link none.
Keys are upper-cased on the way in, and a base URL with no scheme is refused
rather than repaired:

```console
$ roz config set --jira-base-url example.atlassian.net
Error: jira base URL "example.atlassian.net" needs an http or https scheme, e.g. https://example.atlassian.net/browse
```

Nothing here is guessed at. Without a base URL and at least one prefix, Jira
keys render as plain text, because the shape of a key is not distinctive —
`UTF-8`, `SHA-256` and `CVE-2024-1234` all match it, and a link that goes
confidently to the wrong place is worse than no link.

`--db` stays a flag, since it says which database to open. Its value lives on
the command tree that parsed it rather than in a package variable, so two
command trees in one process — which is what `roz mcp` builds, one per tool
call — cannot end up pointed at each other's database. `ROZ_JIRA_BASE_URL`
and `ROZ_JIRA_PREFIXES` override the stored values for a single run.

## Ranking

Creation order is the default everywhere, and it is honest about being
arbitrary. `--sort priority` asks for the real ranking; the page always uses
it.

There is a third order, `--sort staleness`, which answers a different
question and is covered under [Staleness](#staleness).

Those three are rankings rather than columns. `--sort` also takes the
listing's own columns — see
[Ordering by them too](#ordering-by-them-too).

**Projects** sort on one field, `project.priority`, an integer from 1 to 9.
Fewer bands than that are normally in use; the range is wide so that reserving
one — a band for whatever is on fire, say — is a renumber rather than a
migration. Unprioritised sorts last — unstated is not the same as low, but it has to go
somewhere, and behind the stated ones is the reading that does no harm.

```bash
roz project set SL10 --priority 1
```

**Actions** sort on six terms, each breaking ties in the one before:

| # | term | lives on | set with |
|---|---|---|---|
| 1 | `rank_pin` | `action` | `roz action set NA7 --rank-pin 1` (`0` clears it) |
| 2 | `priority` | the `project` it advances | `roz project set SL10 --priority 1` |
| 3 | `rank_class` | the `actionverb` it uses | choose the verb; `roz verb list` shows the classes |
| 4 | unblocks count | computed from `action_blocks` | `roz action add-blocker --from NA8 --to NA7` raises NA7's, since `--from` is the blocked one |
| 5 | `effort` | the `project` it advances | `roz project set SL10 --effort hours` |
| 6 | `n` | `action` | nothing: it is creation order, so the result is stable |

Reading down: the pin exists to override whatever the system worked out, so
it wins outright. Then what you said matters. Then how big a step this is —
`click` before `decide` before `session` before `wait`, so one-click items
clear out and things you are only waiting on sink. Then how much finishing it
frees. Then prefer the project that is nearly done.

**The unblocks count is transitive**, and that is the point of it. The sketch's
argument is that "this week's most important items were the ones gating
chains", and a direct count gives the head of a chain of four the same 1 as
something blocking a single leaf. Only open followers count — freeing
something already done is worth nothing. `hidden_behind` is not part of it:
that edge is the judgement that a follower is not worth looking at, not a
statement about ordering.

Two things are deliberately not terms. **Nothing is inferred from the title or
the age of an item** — a queue that quietly promotes things is a queue you
stop trusting. And **unstated sorts last at every term**, so filling nothing
in never moves an item up.

Two changes from the sketch, both recorded in [`TODO.md`](TODO.md): priority is
a term at all, and it is the first one after the pin. The sketch proposed
`rank_class` → unblocks → effort and predates priority being used as the
planning signal it now is — but a one-click action on a barely-wanted project
outranking real work on the most wanted one reads as the queue ignoring what
it was told.

## When a snooze runs out

A snooze says "hide this until a date". On that date it stops hiding: the item
is back in the queue, marked, with the date it came due.

```console
$ roz action list --unblocked
ID   STATE    VERB    PROJECT  SNOOZED UNTIL   TITLE
NA1  snoozed  decide  -        due 2026-08-05  past its date
NA3  ready    decide  -        -               ordinary
```

It stays `snoozed`. Waking it would rewrite an authored column from a clock —
a different kind of write from any this makes elsewhere — and would throw away
the reason it was deferred, which is often still worth reading even once the
date has gone. The row is unchanged; the queue simply stops pretending the
date has not arrived.

The comparison is against an instant, not a date, so a snooze until the 12th
comes back on the 12th rather than the 13th. `--expired` asks for these
specifically and uses the same definition, so the two cannot disagree about
what is in the queue.

Expect a burst the first time on a queue that has been running a while. That
is the backlog arriving, not a malfunction.

## Staleness

`updated_at` says when something last *changed*. It cannot say when someone
last *looked at it and was satisfied*, and those are different questions: an
item nobody has touched for a month is fine if it was reviewed on Friday and
alarming if it was not.

`roz verify` records the second one, on projects and actions:

```console
$ roz verify ROZ1
ROZ1 verified at 2026-08-11T04:45:50.628Z
$ roz project list --sort staleness
ID    STATUS  PRI  EFFORT  SNOOZED UNTIL  TITLE
ROZ3  active  -    -       -              never checked
ROZ1  active  -    -       -              checked last week
ROZ2  active  -    -       -              checked yesterday
```

**Never checked sorts first**, which inverts the rule everywhere else that
unstated sorts last. It is the same reasoning arriving somewhere different: a
missing priority is an absence of information, while a missing verification
*is* the information — nobody has ever looked at this, so nothing is staler.

`verify` changes nothing else, so it is safe to run on anything at any time,
and running it twice with the same timestamp writes nothing. `--at` backdates,
because the check usually happened before anyone got round to recording it.

It writes an **observed** column, which a person normally may not do.
Verifying is asking the world whether the record is still true rather than
deciding what it should say, which is why the design sketch lists `roz verify`
alongside `roz sync` as the only writers of observed fields. Like
`roz pr announce`, it is a separate command rather than an `--actor`
override — one named verb instead of a hole in the rule — and it is logged as
`sync:verify`, so the log says a person went and looked rather than that
something reported it.

## Short names for repositories

Prose fills up with pull request references, and the natural way to write one
is a prefix and a number. Give a repository a short name and that becomes a
link:

```console
$ roz repo track acme/api-server --short-name api
acme/api-server

$ roz repo list
ID               SHORT  PIPELINE  DEFAULT BRANCH  ANNOUNCE  DISPOSITION
acme/api-server  api    review    -               -         -
```

Prose then writes `api#1234`, and the page renders it as a link to
`acme/api-server/pull/1234` with the text exactly as typed. Nothing rewrites
what is stored — this is a rendering rule, so a short name changed later
changes every mention at once, and a repository with none loses nothing.

The page writes them too. A pull request chip and a tracker issue say
`api#1234` rather than `acme/api-server#1234`, because that is what the short
name is *for*: it is what the repository is called around here, and a column of
repeated owners is a column of noise. The href is the real address either way,
and a repository with no short name keeps its full one.

**Only registered names expand.** `foo#12` stays literal, which is what keeps
the rule from surprising text that was never about a pull request. A short name
is unique across tracked repositories, and a collision is refused when it is
written:

```console
$ roz repo track acme/web --short-name api
Error: the short name "api" is already acme/api-server
```

It cannot contain `/` or `#`, since those are exactly what tell `acme/api#1`
and `api#1` apart. A reference written out in full still wins over a short name
matching its tail, so `acme/api-server#1` is one link and not two.

## Prose the page places

Some of what belongs on a queue page is text nothing can derive: an
introduction, what you have decided matters this week, a standing caveat.

```console
$ roz page set intro --body - <<'MD'
This week is about the merge queue.

- finish the ejection work
- then CODEOWNERS
MD

$ roz page list
SLOT             SET         FIRST LINE
intro            2026-08-12  This week is about the merge queue.
before-queue     -           -
before-projects  -           -
footer           -           -
```

This is not `roz note`, which puts a comment in the log against a subject so
that narrative stays out of the record. This is the opposite case: content that
exists in order to be shown.

A slot is a place on the page, and the set is closed. That is deliberate twice
over. A note filed under a key nothing renders would be invisible rather than
wrong, with no error to say so — so an unknown key is refused. And the page is
worth reading because almost all of it is derived; four places to put prose is
a page with some prose on it, while arbitrary keys would be somewhere to put
anything, which is how that erodes one block at a time.

Notes are Markdown and are linked like every other prose field, so an
introduction naming `SL7` links it. An empty slot draws nothing, not an empty
box, and `page clear` empties rather than deletes — what the page used to say
is in the log.

Each shows when it was last written. A note is authored and nothing revisits
it, so its age is how much to trust it — the same risk `--why` and `--summary`
already carry.

## Defining a pipeline

`roz repo set --pipeline` chooses one, so defining one is a command too — it
was the last piece of configuration that meant writing to the database by hand,
which also meant it never appeared in the log.

```console
$ roz pipeline add fast --label "straight to merge" --steps undraft,merge
fast: undraft → merge
```

**Every step must close on its own.** A pipeline is what follows a person's
work, so a step that waits on a person would stop the chain until somebody
noticed; a `review` step is refused. So is one that needs something
instantiation cannot supply — a `wait_ref` step would be created with nothing
to wait for and would block everything behind it, which is
[#127](https://github.com/scottlaird/roz/issues/127).

**A step can say what it waits for**, which is what makes a release gate
possible. A pipeline is written once and instantiated for every pull request,
so the version has to be relative to wherever the repository has got to:

```console
$ roz pipeline add gated --steps "undraft,wait_ref(>=minor+2),merge"
gated: undraft → wait_ref(>=minor+2) → merge
```

`>=minor+2` reads as "two minor releases on from here". With the repository at
`v2.97.0` the instantiated step waits for `>=2.99.0` — and a series works as it
does for a wait, so `wait_ref(api/>=minor+2)` counts within `api/` alone.

`major` and `patch` count the same way — `>=major+1` is the next major,
`>=patch+1` the next point release. The whole form is described under
[Waiting for the next release, without naming it](#waiting-for-the-next-release-without-naming-it),
where an action can be given one by hand.

A version named outright is refused, because it would be the same gate for
every pull request forever:

```console
$ roz pipeline add fixed --steps "wait_ref(>=3.6)"
Error: ">=3.6" names a version, which would be the same gate for every pull
request; write it relative, e.g. >=minor+2
```

The gate resolves **once**, at the first sync after the chain instantiates, and
is then fixed. It cannot resolve any earlier: what it counts from is a fact
about the repository, and reading GitHub while closing an action would make
closing fail whenever GitHub is slow. Resolving it again on each poll would be
worse — every release that shipped would push the target out by one, and the
gate would never open.

```console
$ roz sync github
cli/cli tag: 200 recorded on the first poll
NA3 waits for >=2.99.0 (>=minor+2)
```

The queue item is retitled to match, so it says what is being waited for rather
than how it was worked out. A title you have since edited is left alone.

**No steps is a legal chain**, and is what a repository whose pull requests you
only review wants: closing the `review` action should produce nothing rather
than an undraft-and-merge chain for somebody else's work.

```console
$ roz pipeline add reviewing --label "somebody else's" --steps ""
reviewing: no steps; nothing follows
```

That is not the same as a repository with no pipeline set. Both instantiate
nothing today; one is a decision and the other is a gap.

**Where it lands is explicit**, because the lowest-numbered active pipeline is
what a newly tracked repository takes. Adding one never moves that from under
you, and `--order first` says out loud that it has:

```console
$ roz pipeline add house --steps undraft,merge --order first
house: undraft → merge
  newly tracked repositories will take it
```

Retiring is not deleting, the way it is for a verb. A repository still naming a
retired pipeline keeps working — its chains instantiate exactly as before — and
only new repositories stop taking it:

```console
$ roz pipeline retire fast
fast is retired
  owner/repo still names it, and keeps working
```

Editing a pipeline never touches a chain already running. Steps are copied into
actions when a chain starts, so nothing reads the pipeline again afterwards.

## The page links to itself

Every action and project has an anchor, which is its identifier verbatim:
`#SL7`, `#NA57`. Prefixes are configurable, so a scheme like `project-SL7`
would have to know which prefix means which kind — and the identifier is
already unique across both, since the two prefixes cannot be the same. It is
also what anyone would guess, which matters for something that ends up in
other people's notes.

An action's project is a link. A reference in prose becomes one too, when the
thing it names exists:

```markdown
waits for SL21, and for SL999 which is nobody
```

`SL21` links; `SL999` stays text. **Existence is the whole rule.** A pattern
loose enough to catch `NA57` also catches `UTF8` and `SHA256`, and nothing but
asking whether the thing exists tells them apart. It also means a reference
written before its target was created simply reads as text until it is.

`[[SL21]]` is the explicit form, for when the bare one would be ambiguous. It
links the same way, and loses its brackets either way: a reference to something
that does not exist renders as its own text, since the brackets were markup
asking for a link.

**A backticked identifier does not link.** `` `SL21` `` is a code span, and
code spans are literal — the same rule that keeps `` `api#1234` `` from
expanding. If a reference should link, write it bare or in double brackets.

### Everything has somewhere to land

The queue is a subset, so most identifiers name something not in it. Below the
blocks are two index tables holding every action and every project the blocks
leave out — closed, blocked, hidden, deferred.

Each entity appears **exactly once across the whole page**, which is what makes
an anchor unique and lets a link ignore which section its target ended up in. A
snooze still keeps something out of the queue; it does not keep it off the page,
because a reference to a deferred item needs a destination as much as any other.

## What a link points at

Every link the page draws to something roz tracks carries a tooltip taken from
the record it points at: a pull request's title, a Jira issue's summary.

```html
<a href="https://github.com/acme/api/pull/1234"
   title="Retry the upstream call on 503">acme/api#1234</a>
```

The annotation keys off **where the link goes**, not how it was written, and
that is the part worth knowing. A link written out in full keeps whatever
display text its author chose and is still captioned:

```markdown
see [cp#1234](https://github.com/acme/api/pull/1234)
```

So there is no shorthand syntax to invent for this. A short form like `cp#1234`
could not be resolved anyway — the alias is not knowable — while the full link
is unambiguous, and an author who wants the short label can simply write it as
the link text.

Titles are read at render, so a renamed pull request shows its new title with
nothing re-synced. Anything untracked, or tracked but never observed, gets no
tooltip at all: a blank one would say roz looked and found nothing, which is a
worse thing to claim on a hover than saying nothing. Long titles are cut at
about ninety characters on a word boundary, because a tooltip is a glance.

The chips beside an action follow the same rule, and by the same means: the
pull requests it is about and the issues its project tracks carry the title or
summary last observed. A tracker issue also keeps its status where it has one,
as a span of its own rather than folded into the tooltip — the status is what
you read, the summary is what you hover for.

## Prose fields

Some fields are written as sentences rather than as values, and those are
Markdown: `action --why`, `project --summary`, both `--snooze-reason`s, and a
calendar note. A title is *not* — it is a name, so asterisks in one stay
asterisks.

```console
$ roz action set NA7 --why 'unblocks the *split*, once `roz sync github` runs'
NA7 why: "" → "unblocks the *split*, once `roz sync github` runs"
```

The database keeps what you typed. `show -o json` returns the source, the
event log records the source, and only `roz render` turns it into HTML — so
nothing is lost if you decide later that a field should have been plain.

On the page, an action's `--why` sits under its title in the queue, and a
project's `--summary` gets a row of its own beneath the project, spanning the
table. A summary is prose and does not fit a cell.

Identifiers are linked wherever they appear, including inside a sentence:
`CDSS-1557` and `scottlaird/roz#54` both become links, while a bare `#54`
does not, because which repository it means is a guess. Linking happens on the
parsed document rather than on the text, so an identifier inside a code span
or inside a link you wrote yourself is left alone.

The one thing rejected on input is raw HTML:

```console
$ roz action set NA7 --why 'see <b>this</b>'
Error: action.why: raw HTML is not allowed here: "<b>" — write it as Markdown, or wrap it in backticks to show it literally
```

Refusing it here rather than stripping it at render time means you find out
straight away, instead of wondering later why half a sentence is missing from
the page.

## Choosing what a listing shows

A listing shows a default view, which is a deliberate subset. `--fields` names
the columns instead, and `--fields all` shows every column the record has:

```console
$ roz ref list --fields name,commit_sha
NAME     COMMIT
v2.96.0  abc12345
v2.97.0  def67890
$ roz ref list --fields all
ID                         REPOSITORY  NAME     KIND  COMMIT    FIRST SEEN                OBSERVED AT
cli/cli@refs/tags/v2.96.0  cli/cli     v2.96.0  tag   abc12345  2026-08-01T00:00:00.000Z  2026-08-01T00:00:00.000Z
```

The columns are the record's own, read off the same struct tags the `SELECT`
and the JSON encoder read — so a column added to an entity is available here
without anything being told about it. A listing only declares what it shows by
default and what it shows differently.

`-o csv` writes the same selection for something else to read:

```console
$ roz ref list --fields name,commit_sha -o csv
name,commit_sha
v2.96.0,abc1234567890def1234567890abcdef12345678
```

**A table is for a reader and the other two are for a program**, which is the
one place they differ. A table abbreviates a commit to eight characters
because that is how anybody reads one; CSV and JSON carry the commit. The
header follows the same split — `COMMIT` for a person, `commit_sha` for a
column name something will match on.

Choosing fewer columns therefore loses columns and never alters one.
`-o json` keeps carrying everything until you ask it not to, so a caller that
parses it does not silently lose a field because a table's default view has no
room for it.

**Every listing takes both**: `action`, `project`, `pr`, `issue`, `ref`,
`repo`, `calendar`, `verb` and `pipeline`.

A column that only matters sometimes stays out of the default view until it
does — `pr list` grows a `MERGED` column when something in it has merged, and
`BECAUSE` when something says why it is tracked. Asking for one by name shows
it whatever is in it, because asking is the answer to whether it is relevant.

### Ordering by them too

`--sort` takes the same column names, primary key first, and a `-` reverses
one:

```console
$ roz pr list --since 2026-08-06 --sort -merged_at
$ roz project list --sort status,title
```

**NULLs sort last whichever way a key runs.** SQLite leads with them going up
and trails them coming down, which would open "sort by when it merged" with
everything that never did. Absence is not a small value and it is not a large
one; it is the least interesting thing in the column.

**`created`, `priority` and `staleness` are reserved words**, not columns.
`priority` is a CTE, two joins and an expression over three tables; `staleness`
is a computed date. They live in the same flag because they are what somebody
actually types — splitting them off would mean remembering which flag a given
ordering is behind — but they cannot be *keys* in a list of them, since a
ranking is the whole ordering rather than the first term of one:

```console
$ roz action list --sort priority,title
Error: --sort priority is an ordering of its own and takes no further keys;
sort by columns instead, or by priority alone
```

**Only real columns sort.** A derived column has nothing behind it to order on,
and the error says what there was rather than leaving you to guess:

```console
$ roz action list --sort late
Error: --sort "late" is not a column to sort on: use id, kind, n, title, verb,
… ; or one of created, priority, staleness
```

That check is also what keeps the names safe. SQLite cannot bind a column name
as a query parameter, so an ordering is text going into an `ORDER BY` — and the
only text that gets there came off a record's own struct tags.

`--sort` works with `--tree`, where it orders the roots and each parent's
children without flattening the shape.

## For an agent

`roz mcp` serves the same commands over the Model Context Protocol, on stdin
and stdout, for an agent to call without shelling out.

**The tools are the commands.** They are derived from the command tree rather
than written out again, so the two cannot drift: the name is the command path
with an underscore (`action add` → `action_add`), the description is that
command's own help, and the arguments are its flags and whatever its usage
line names. Forty-six of them:

| | |
|---|---|
| settings | `config_show` `config_set` |
| projects | `project_add` `project_show` `project_list` `project_set` `project_snooze` `project_wake` `project_supersede` `project_close` `project_link-issue` `project_unlink-issue` |
| actions | `action_add` `action_show` `action_list` `action_set` `action_snooze` `action_wake` `action_add-blocker` `action_hide-behind` `action_link-pr` `action_close` |
| GitHub | `repo_track` `repo_show` `repo_list` `repo_set` `pr_track` `pr_set` `pr_show` `pr_list` `pr_announce` `sync` |
| the log | `note` `exception` `watch` (bounded to one read) |
| tracker issues | `issue_show` `issue_list` `issue_observe` |
| other | `calendar_add` `calendar_show` `calendar_list` `calendar_set` `verb_list` `pipeline_list` `render` `verify` |

**Left out**, because they are not an agent's to call: `init`, which decides
where the database lives, and `serve`, `syncer` and `mcp`, which never return.

`watch` **is** offered, as the bounded read: the server forces `--once` and
hides `--interval`, so it answers with the events matching the filters and
returns rather than following. "What has happened since" is the useful
question, and an agent can ask it again — `--since`, or `-n` for a backlog
count.

**Every change is recorded as `agent:<name>`**, taken from what the client
calls itself when it connects — `Claude Code` becomes `agent:claude-code` —
and falling back to `--agent` otherwise. Neither the actor nor the database is
offered as a tool argument, so a call cannot write as a person or land in a
different database.

```jsonc
// → {"jsonrpc":"2.0","id":1,"method":"tools/call",
//    "params":{"name":"project_add","arguments":{"title":"from the agent","priority":2}}}
// ← ROZ1
```

## Continuous integration

CI runs on every pull request and on `main`: `gofmt`, `go vet`, and the tests
under the race detector. Only under the race detector — it runs the same tests,
and it is the run that matters, because `serve` renders the page on one
goroutine while the watcher reads the log on another.

```
gofmt -l . && go vet ./... && go test -race ./...
```

Nothing in the suite reaches the network or the real `gh`: GitHub reads go
through an injected runner, and the store tests open a fresh database per test
with the clock advanced a second per transaction.

## When one pull request is different

A repository's pipeline is the usual answer — right almost always, and wrong
exactly when it matters. A hotfix that skips review, or a change to protected
code needing more than the usual steps, says so for itself:

```console
$ roz pr track scottlaird/roz#1 --pipeline direct
scottlaird/roz#1
$ roz pr list
ID                STATE  DRAFT  REVIEW  MERGE  CHECKS  FROZEN  PIPELINE  TITLE
scottlaird/roz#1  -      -      -       -      -       no      direct    -
scottlaird/roz#2  -      -      -       -      -       no      -         -
```

The `PIPELINE` column appears only when something is using it. Unset is the
ordinary case and means the repository's — **read when the chain is
instantiated, not copied at track time**, so changing a repository's policy
reaches the pull requests that never claimed an exception to it. That is
deliberately unlike `repo track`, which resolves the *default* eagerly: a
default is a guess made in the absence of policy, and freezing it protects
repositories already tracked from a pipeline being retired or reordered
underneath them.

Learning a pull request is a hotfix after tracking it is the common case, so
the decision is revisable, and an empty value gives it back:

```console
$ roz pr set scottlaird/roz#1 --pipeline ""
scottlaird/roz#1 pipeline: "direct" → ""
```

Changing it affects the chain the next close instantiates. Actions already
created are left alone — they exist, and something may already be waiting on
them.

## Pull request checks

GitHub reports a conclusion per check context. Those are rows in `pr_check`,
one per check, rather than a single JSON column — and the difference is what
the log says when one moves:

```
2026-08-11T05:01:00.000Z  info  sync:github  changed  scottlaird/roz#67  checks/test: "SUCCESS" → "FAILURE"
```

The blob could only ever diff as "the map changed", so every job starting or
finishing re-emitted the lot: about fifteen events an hour across two pull
requests, none of them saying anything worth reading.

**Most transitions are written and not logged.** Going green is the common
case and burying a real failure underneath it is the harm this exists to
stop, so only crossing into a broken state — `FAILURE`, `ERROR`, `CANCELLED`,
`TIMED_OUT` — and crossing back out of one produce an event. A check that was
already failing and still is produces nothing, which is what tells *newly
broken* from *still broken*. `PENDING` is not broken; a check that has not
finished is not a failure.

That is the same trade `pr.last_synced_at` already makes as an `auto` column:
written because it is true, unlogged because it would drown the log.

`checks_state` stays on `pr` — GitHub's rollup, and what the page and
`pr list` show. The per-check detail is on `pr show`.

## Why a pull request is tracked

The row's existence records the *decision* to track a pull request. It cannot
record the reason, and that only starts mattering once you track one you did
not write: a review has different actions, and a different reason to stop
tracking it, from your own work.

```console
$ roz pr track scottlaird/roz#2 --because reviewing
scottlaird/roz#2
$ roz pr list --because reviewing
ID                STATE  DRAFT  REVIEW  MERGE  CHECKS  FROZEN  BECAUSE    TITLE
scottlaird/roz#2  -      -      -       -      -       no      reviewing  -
```

Three reasons, as a closed set: `authored` (you wrote it), `reviewing`
(somebody wants your review), `watching` (neither, but you care). A CHECK
rather than free text, because things branch on it — adding a fourth is a
migration, which for a vocabulary this small is the right trade.

**There is no default.** Assuming `authored` would be right most of the time
and would still be the tool inventing a fact it cannot check. Unstated is the
honest answer to a question nobody was asked, and `--because ""` returns it
there.

The `BECAUSE` column appears in `pr list` only when something is using it,
the same rule `PIPELINE` follows: an exception is worth seeing and its absence
is not.

This does not finish the `review` verb. Closing that on a predicate needs our
GitHub login as well, and `config.owner` is deliberately a label rather than
one.

## The week in review

The end of a week is two questions: what did I merge, and what finished. Both
are windows over what is already recorded, ordered by when it happened.

```console
$ roz pr list --since 2026-08-06
ID                  STATE   DRAFT  REVIEW  MERGE  CHECKS   FROZEN  MERGED      TITLE
scottlaird/roz#161  MERGED  no     -       -      SUCCESS  yes     2026-08-06  Colour the queue
scottlaird/roz#164  MERGED  no     -       -      SUCCESS  yes     2026-08-07  Quiet the issue polls
$ roz issue list --since 2026-08-06
ID              STATUS  ITERATION  ASSIGNEE  CLOSED      SUMMARY
jira:CDSS-1744  Done    -          scott     2026-08-07  Split the nodepool
```

`MERGED` and `CLOSED` appear only when something in the listing has one, the
same rule `BECAUSE` and `PIPELINE` follow: a listing of open work has nothing
to say there.

**Oldest first**, because a week is read in the order it happened. That is the
one place these listings depart from their usual order — `pr list` normally
groups by repository, which answers a different question and answers this one
badly.

`--since` reads the *finishing* time, so it selects merged pull requests and
closed issues on its own. `--state MERGED` alongside it is redundant rather
than wrong, and on its own gives the same order over every merge ever tracked.
`--closed` is the issue equivalent: everything that has finished, with no
window.

### When it happened, not when roz noticed

`merged_at` and `closed_at` are GitHub's own timestamps, stored rather than
worked out from the log.

The log looks like it should answer this — it records the transition to
`MERGED`, with a time — and it is the wrong answer twice over. A pull request
tracked *after* it merged has no transition at all: it was already merged the
first time roz read it, so nothing moved. One that merged while roz was not
running transitions at the poll that caught up, which dates it to whenever the
laptop was next opened. Both land in the wrong week, and the second lands there
silently.

So the fact is taken from whoever owns it. GitHub reports `mergedAt` and
`closedAt` on every poll, and roz stores what it is told.

### What this cannot see

An issue only appears in `--closed` once something has recorded *when* it
closed. GitHub supplies that on every sync. Nothing reads Jira, so a Jira issue
has it only where it was given one:

```console
$ roz issue observe CDSS-1744 --status Done --closed-at 2026-08-07
```

That is deliberately not inferred from the status. `Done`, `Closed`,
`Resolved` and `Shipped` are four trackers' words for one idea and `Won't Fix`
is a fifth that means something else, and picking which of them counts as
finished would be roz deciding what somebody else's workflow means. The status
column is unconstrained on purpose for exactly that reason.

An issue closed in Jira and never recorded as closed is therefore missing from
the week rather than misdated in it — roz has not been told, which is a better
failure than a confident wrong answer.

**Issues that had pull requests merged against them** are the third question a
week wants, and roz cannot answer it: the association lives in the pull request
body and nothing parses it. That is
[#167](https://github.com/scottlaird/roz/issues/167).

## Projects inside projects

Forty projects is a list. The handful of things they are actually about is what
you wanted to read:

```console
$ roz project list --tree --sort priority
ID    STATUS  PRI  EFFORT  SNOOZED UNTIL  TITLE
SL1   active  1    -       -              Platform
SL2   active  2    -       -                Storage
SL3   active  1    -       -                  Sharding
SL4   active  3    -       -              Docs
```

Set with `--parent` when a project is created or afterwards, and cleared with
`--parent ""`.

**Display only.** A parent does not block a child, closing a parent does not
close its children, and nothing about priority or ranking reads it. Those are
relationships roz already has — [one project waiting on
another](#one-project-waiting-on-another) says one must finish before the next
starts — and conflating "is part of" with "waits for" would make both mean
less.

The order inside the shape is still the order you asked for: `--sort priority`
sorts each level, rather than being replaced by the hierarchy. **Flat is the
default**, because most projects have no parent and a hierarchy of one level is
a list with ceremony. The page draws the hierarchy always.

A cycle is refused, and not only the obvious one — the schema can see a project
that is its own parent, and nothing more, so the chain is walked before writing:

```console
$ roz project set SL1 --parent SL3
Error: SL3 cannot be a parent of SL1: SL3 is already under it, through SL3 → SL2 → SL1
```

**A closed parent is still drawn when open work sits under it**, greyed on the
page and marked in the listing, because hiding it would orphan its children —
the opposite of what filtering to open work asked for:

```console
$ roz project list --tree --status active
SL1   active  1    -  -  Platform
SL2   done    2    -  -    Storage  (closed)
SL3   active  1    -  -      Sharding
```

That needs no count of open descendants: a closed project whose children are
all closed is not an ancestor of anything in the list, so nothing pulls it in.

## One project waiting on another

`project.status` has accepted `blocked` since the start, and nothing recorded
what it was blocked *on* — `action_blocks` is action-to-action. So it was a
status with no referent: the dependency lived in a summary, the project
vanished from the page, and nothing ever cleared it.

```console
$ roz project block --from ROZ2 --to ROZ1
ROZ2 is blocked, waiting on ROZ1
$ roz project list
ID    STATUS   PRI  EFFORT  SNOOZED UNTIL  TITLE
ROZ1  active   -    -       -              Thread the --db flag through
ROZ2  blocked  -    -       -              MCP over HTTP
$ roz project close ROZ1 --status done
ROZ1 done
  ROZ2 is now active
```

`--from` is the blocked one and `--to` is its blocker, the same way
`action add-blocker` reads. The status moves with the edge, and only the
*last* open blocker closing frees it.

**A blocked project stays on the page**, marked. Blocked is exactly where work
goes quiet, which makes it the last thing worth hiding.

### Its actions leave the queue with it

The queue answers *"what do I do now"*, and a blocked project's actions are
exactly the ones that cannot be done now. So they come out of
`action list --unblocked`, and stay in the full listing, marked:

```console
$ roz action list --unblocked
ID   STATE  VERB   PROJECT  SNOOZED UNTIL  LATE  TITLE
NA3  ready  write  ROZ2     -              -     Unrelated work
$ roz action list
ID   STATE  VERB    PROJECT  SNOOZED UNTIL  LATE  HELD  TITLE
NA1  ready  write   ROZ1     -              -     yes   Write the endpoint
NA2  ready  decide  ROZ1     -              -     yes   Decide the cutover
NA3  ready  write   ROZ2     -              -     -     Unrelated work
```

`action show` names what is holding it, since the action itself carries
nothing that would explain it:

```console
$ roz action show NA1
...
held_by     ROZ2
project_id  ROZ1
state       ready
```

**Nothing is written onto the action.** It is read from the project's status
at query time, which is what makes the release free: an action has
`blocked_by` and `hidden_behind` already, each with its own release condition,
and a third writer of the same state would raise the question of which one
lets go. There is nothing to let go of here — the moment the last blocker
closes, the project is active and its actions are back, with nobody having
remembered which ones they were.

It reads `project.status` rather than counting open blockers, so this and
`project list` cannot disagree. That disagreement was the bug
([#181](https://github.com/scottlaird/roz/issues/181)).

**`rank_pin` is the escape.** Not every action on a blocked project is blocked
by the same thing — the `decide` that would *remove* the blocker is exactly
the work that clears it, and a rule applied uniformly buries it:

```console
$ roz action set NA2 --rank-pin 1
$ roz action list --unblocked
ID   STATE  VERB    PROJECT  SNOOZED UNTIL  LATE  HELD  TITLE
NA2  ready  decide  ROZ1     -              -     yes   Decide the cutover
NA3  ready  write   ROZ2     -              -     -     Unrelated work
```

Pinning says *"I mean this one"*, which is what `rank_pin` has always meant.
The `HELD` mark stays, because it is still true and worth knowing.

`--waiting` is unaffected. It answers what you are waiting *on*, and a wait for
a review is still true whatever the project's status says.

`roz project unblock` removes the edge for when the dependency was wrong
rather than satisfied — saying so should not mean closing something unfinished.

A snooze outranks the graph: a project deferred to a date is not un-deferred
by an edge, because a snooze is a decision about time. There is no project
equivalent of `hide-behind`, since folding something out of a queue is a
judgement about a queue and the project table is not one.

## Who a pull request needs

A `wait_review` action can say a pull request is waiting for review. Saying
*who* it waits for is what tells you whether to chase it, so sync works that
out: the files the change touches, matched against the CODEOWNERS on the branch
it targets.

```console
$ roz sync github
linuxcnc-ethercat/linuxcnc-ethercat#510 needs @grandixximo
polled 1, 1 changed
```

That is a repository which does **not** enforce CODEOWNERS through branch
protection, so GitHub requested nobody and `reviewDecision` is null — while the
file still describes who ought to look. Deriving answers a question GitHub does
not answer at all for those, which is most of the reason it earns its place.
Where a repository *does* enforce it, GitHub's own `reviewRequests` is the
better source and is still what the page shows first.

It is an **observation with a time**, not a fact about the repository.
Ownership is per-directory and CODEOWNERS changes underneath a long-lived pull
request, so the log carries when each answer was true:

```console
$ roz watch --once -n 2
… required_owners: "[]" → "[\"@grandixximo\"]"
… owners_head: "" → "3b409ca4b0892c496d1af6e91c28f25add3a5cf0"
```

`owners_head` is what makes this affordable. The read is a paginated file list
plus a CODEOWNERS fetch, per pull request — far more than the batched state
query — so it runs only where the head has moved since the last answer. Which
files a change touches cannot change while the change does not. A push that
moves the head without changing who is needed records the new head and reports
nothing: it was checked, not changed.

The set of owners is stored, not the mapping of every path to its owner. A
large pull request names half an organisation, and "who owns line 40 of the
generated mock" is a live read away — see below.

## Who has to approve this

CODEOWNERS says who owns which paths. What it does not say — and what a list
of the owners a change mentions cannot tell you — is whether one person could
approve the whole thing, and once somebody has, which of the rest would
actually help.

```console
$ gh pr diff 123 --name-only | roz codeowners --owners CODEOWNERS
files       5
unowned     1
any one of  -  no single owner covers every file

would cover
  @org/platform  2 of 4
  @org/api       1 of 4
  @org/storage   1 of 4

fewest approvals: @org/platform @org/api @org/storage
```

The reduction is the point. Rules overlap: one owner has the repository, a
second carves out a directory, a third owns a glob reaching back into it. Only
the last rule matching a path decides it, so which owners a change genuinely
needs is a question about the file-to-owner map rather than about the file.

A review arrives naming a person, and the files are owned by teams, so an
approval is expanded into every owner it satisfies — the person, and each team
they belong to:

```console
$ ... | roz codeowners --owners CODEOWNERS --team org/platform=alice,bob --approved bob
approved as  @bob @org/platform
outstanding  2

would cover
  @org/api      1 of 2
  @org/storage  1 of 2
```

`@org/platform` is gone from that list: asking them again would achieve
nothing. Somebody in two owning teams satisfies both at once, which is why one
review can finish a change that has no sole approver at team granularity.

`--pr` fetches all three from GitHub instead — the changed files, the
CODEOWNERS on the base branch, and who has already approved:

```console
$ roz codeowners --pr cli/cli#14130
owners  .github/CODEOWNERS@trunk
files       16
any one of  @cli/code-reviewers

would cover
  @cli/code-reviewers  16 of 16
```

The rules come from the **base branch**, not the default one: a change into a
release branch is governed by that branch's file, and the output names the ref
it used so it is clear which.

The file list is paginated to completion rather than capped. A missing file
could turn "no single owner covers this" into "one does", and being wrong in
that direction is worse than being slow.

This is deliberately not part of the poll. The file list is large, changes only
when someone pushes, and is wanted when a person asks — so paying for it per
pull request per minute would be the wrong trade.

`--team` is still a stand-in, and the command says so when it matters:

```console
note: team membership is not resolved yet, so an approval only satisfies the
person who gave it; pass --team to supply it
```

Resolving a login to its teams needs an org read the rest of this does not, so
`github.Client.TeamMembers` is a stub with the contract written and the query
sketched. The wiring around it is finished: filling in that one function makes
`--pr` expand approvals on its own, with no flag to remember. Until then the
rest of the answer is printed rather than withheld — which owners exist, what
is unowned, whether one owner covers everything — because those are correct
without it.

`--team` overrides whatever is fetched, and `--approved` adds to whatever the
pull request already reports rather than replacing it.

## Who to ask

`roz codeowners` says who *could* approve a change. Who to *ask* is a different
question, and it was folklore — somebody knows that changes here go to this
team first, and nothing wrote it down.

```console
$ roz repo prefer acme/api --prefer @org/platform,@org/storage
acme/api prefers @org/platform → @org/storage

$ roz codeowners --pr acme/api#812
ask, in order
  1. @org/platform   9 files  preferred, and covers outstanding files
  2. @org/storage    3 files  preferred, and covers outstanding files
```

A preference, never an assertion. Each hint is **checked against what is
actually outstanding** before it is used, so one that owns nothing in a
particular change is skipped rather than asked:

```console
$ roz codeowners --pr linuxcnc-ethercat/linuxcnc-ethercat#510
ask, in order
  1. @grandixximo  65 files  covers outstanding files
```

`@scottlaird` is hinted there and does not appear, because that repository has
two `*` rules and last-match-wins, so it owns nothing. That check is what stops
a hint becoming a habit nobody revisits: the team that used to own this and has
not for a year drops out of the answer on its own.

**A hint need not appear in CODEOWNERS at all.** A team whose members all
belong to an owning team is a usable ask, because the approval it produces
satisfies the rule:

```
  1. @org/storage-oncall  4 files  preferred, and its members all belong to an
                                   owner (@org/storage)
```

That is a question about membership rather than names, and it needs GitHub to
answer. Where the membership read fails, routing carries on from CODEOWNERS
alone and only this case is lost.

A team that *partly* overlaps an owner is not routed to. "This might help,
depending which member replies" is not something a plan should promise, and a
partial overlap is exactly that. It is not wrong to ask them; it is wrong to
say they will do.

**Hints never override CODEOWNERS.** Ordering what is already required is safe;
substituting for a required owner is not, so routing continues until the rules
are satisfied whatever the hints said. Where nothing is hinted, the choice is
the owner covering the most outstanding files.

## When a pull request falls out of the merge queue

An ejected pull request is the quietest way for finished work to stall. It
looks exactly like one that was never queued — approved, `CLEAN`, every check
green — and nothing is waiting on anyone.

```console
$ roz sync github
owner/repo#1 in_merge_queue: "1" → "0"
owner/repo#1 left the merge queue without merging
  NA9 added to the queue
polled 1, 1 changed, 1 ejected
```

The signal is `in_merge_queue` going true → false **while the pull request is
still open**. The common version of that transition is a merge, so the state
has to be checked: keying on the transition alone would raise an exception on
every pull request that lands.

The action is a `merge`, which means it closes itself when the pull request
eventually does. Re-queuing is not always the answer — a base branch moving, a
required check re-running, or another pull request failing a batch can all
eject this one — but merging is what the item is waiting for either way, and an
item that cannot close on its own is one somebody has to tidy up.

Nothing is added when an open action already covers merging that pull request.
A queue that reshuffles can eject and re-add within a minute, and a tracked
pull request usually has a `merge` step already; the ejection is news, but the
thing to do about it is on the list.

The exception is logged as `sync:github` and the action written as `predicate`
— two claims, and only the first of them is an observation.

## When a wait goes on too long

The queue leaves out what you are only waiting on — four of ten items were
waits, and a queue full of things you cannot act on is not a queue. That was
right, and it left nothing speaking up when a wait went bad.

```console
$ roz sync github
NA1 has been waiting 9 days: wait for review on the nodepool split
polled 0, 0 changed, 1 overdue
$ roz watch --once -n 1
2026-08-11T05:30:35.567Z  exception  predicate  waited_too_long  NA1  wait_review for 9 days, past 2026-08-04 09:00:00
```

It is an `exception`, which is what a monitor already filters on, rather than
a second alerting path.

An overdue wait also puts something in the queue:

```console
$ roz sync github
NA1 has been waiting 11 days: wait for review owner/repo#1
  NA3 added to the queue
polled 1, 0 changed, 1 overdue
```

That is the half that reaches a person. An exception is durable and queryable,
and neither of those is something anybody does on a Monday morning; an action
is simply there. The verb is `decide`, and human-closed on purpose: "I looked
at this and it is fine" is a legitimate outcome, so closing must not wait for
the condition to go away.

One action per condition — the pair of an exception kind and what it is about —
counted whether the action is open or closed. Closed has to count: a pull
request that has gone invisible is reported on every poll, so raising again
once the last was closed would make the item impossible to clear. A queue entry
with no off switch is worse than none, because it teaches you to ignore the
queue. The cost is that a condition recurring long after it was dealt with
produces nothing new, and the log still has every firing.

An overdue item gets a second one *only if the queue leaves it out*. A wait is
excluded by design, so without a `decide` there would be nothing at all to see;
anything else is already in the queue, and a second row about it would be two
items for one job — the second not clearable by doing the first. The test is
the rank class, not how the verb closes: `merge` closes on a predicate, carries
an allowance of a day, and sits in the queue like anything else.

Those are marked rather than duplicated, in the listing and on the page:

```console
$ roz action list
ID   STATE  VERB   PROJECT  SNOOZED UNTIL  LATE     TITLE
NA1  ready  merge  -        -              11 days  merge the thing
```

Being in that column is what says late; the number only says by how much. A
deadline missed an hour ago is nought days past it and still missed.

**Reported, never changed.** Nothing is closed, snoozed or reprioritised —
what to do about a stuck wait is a judgement, and this only says one is
wanted. **It is not a snooze:** a snooze hides something until a date, this
reveals something after one.

The allowance lives on the verb, as `wait_days`, because how long is
reasonable is a property of the kind of waiting rather than of the item.
`wait_review` gets three days and `merge` one; every other verb is NULL and
never times out, which is right for the ones describing your own work —
nothing is waiting, so nothing can be overdue.

```bash
roz action set NA1 --okay-to-wait-until 2026-09-01
```

That is the exception for the one that is different, in either direction. An
empty value clears it and the verb's allowance applies again.

The allowance itself is tunable, because it is a judgement about one person's
queue rather than a change to what the verb means:

```console
$ roz verb set wait_review --wait-days 1
wait_review wait_days: "3" → "1"
```

It is read when the deadline is checked, so the next pass uses the new number.
Verb rows are authored, so the change is in the log like any other. **Calendar
days, not working days** — at 1, a wait that starts on Friday is overdue on
Saturday, which is tolerable only because an overdue wait becomes something
sitting in the queue on Monday rather than something demanding attention when
it fires.

`rank_class` is tunable the same way. What a verb *means* is not: `closes` and
the predicate it names are checked against the build when the database opens,
so a verb naming a predicate this binary lacks is refused at startup. Editing
those from the CLI would turn that check into a failure at closing time.

The clock is `waiting_since` — when reviewers could first have seen it, which
sync fills from the pull request's first review request — falling back to when
the action was created where nothing has observed a wait beginning.

A wait is reported once. The log is the record of that, so nothing else has to
remember; and if the deadline moves out because a fresh review was requested,
it is reported again, because it is a different wait.

### A standing condition is reported once a day

Some exceptions describe a situation rather than an event: a pull request that
has gone invisible, a repository too large for the ref feed. Sync cannot tell
"this just became true" from "this is still true" — it re-derives the world
every few seconds and finds the same thing each time — so those are logged once
and then stay quiet for a day.

The key is the condition: the exception kind, and what it is about. Two
problems on one repository are two conditions and both surface; rewording a
message does not defeat the suppression, and neither does a detail moving, like
a ref count creeping up. A condition still outstanding tomorrow is mentioned
again, because by then the first notice has scrolled out of view.

The first occurrence is never delayed. Suppressing repeats is the point;
suppressing the signal would be a different bug.

`roz exception` is unaffected — a person recording one deliberately is not a
poll restating itself.

## Waiting for a release

Waiting for a release used to be a snooze to a guessed date. That is wrong in
both directions: if the release slips the action wakes early and gets
re-snoozed, and if it ships early it sleeps through the thing it was waiting
for. A date was standing in for a condition, and closing on conditions is the
one thing roz already knows how to do.

```console
$ roz action add --title "Ship the migration once cli 2.98 is out" --verb wait_ref \
    --ref-repo cli/cli --ref '>=2.98'
NA1
```

`--ref` is a [semver constraint](https://github.com/Masterminds/semver#checking-version-constraints),
not a name, because when you write the block nobody knows whether the next
release is `v2.98.0` or `v2.98.1`. The usual operators all work — `>=1.5`,
`^1.2`, `~1.2.3`, `1.2.x`, `>=1.2, <2.0`.

Sync polls only the refs something is waiting for. A repository with no
outstanding wait is never asked, so this costs nothing until it is relevant,
and there is no watch list to keep in step with the waits themselves.

```console
$ roz sync github
cli/cli tag: 200 recorded on the first poll
polled 0, 0 changed, 1 ref queries
```

A repository's first poll sees its whole tag history at once. None of that
*appeared* in any sense a person means, so it is counted rather than listed;
after that, a tag turning up is one line and is news.

Only the first read of a repository walks its history. After that a poll stops
as soon as it recognises a ref it already has — tags come back newest-first, so
meeting a known one means the read has caught up and everything below is older
still. A repository with eight hundred tags and nothing new costs one request.

Refs are read in pages, to a bound. **Tags** come back newest-commit-first, so a
forward-looking wait is answered by the first page. **Branches** have no commit
date to order by — GitHub offers only alphabetical or tag-commit-date — so they
are read alphabetically, which has nothing to do with recency, and what makes a
branch wait work is the filter rather than the order. `facebook/react` has 945
branches whose release ones sort well past any bounded read; asking GitHub for
`releases/` narrows that to three.

A series is asked for as a **ref prefix**, so it returns exactly what is under
it. The substring filter GitHub also offers is approximate — asking it for
`service/s3/` in `aws/aws-sdk-go-v2` returns 321 refs where a prefix returns
the 301 that are actually there — and it is kept only for narrowing *within* a
series, where a name-shaped expression has a literal head worth using. A
constraint contributes nothing to it, since `>=1.2` is not a substring of any
ref name.

A **top-level** series cannot be narrowed at all: GitHub insists a ref prefix
end in a slash, so there is no way to ask for "tags starting with v", and a
substring that short matches almost everything. A top-level wait against a
monorepo therefore reads the whole namespace.

### Tags and branches are read differently

Tags come back **newest-first**, so a wait for a release that has not happened
yet is answered by the first page however long the history is. That is why a
repository with 82,000 tags still costs one request per poll: the read stops as
soon as it recognises a tag it already has, and everything below is older.

Branches have no commit date to order by — GitHub offers alphabetical or
tag-commit-date and nothing else — so they come back **alphabetically**, which
has nothing to do with recency. A new branch sorts wherever its name falls, so
a branch read cannot stop early: the first page would stay old and familiar for
ever, hiding anything named late in the alphabet. Branch namespaces are
normally small enough that this costs nothing — `aws/aws-sdk-go-v2` has 32
branches against its 82,241 tags — and where one is not, the filter is what
keeps it cheap.

Practically: **scope a branch wait to a directory**, and the whole question
goes away. `releases/19.2.x` asks GitHub for `refs/heads/releases/` and reads
three branches in `facebook/react`, where the unscoped namespace holds 949.
Release branches are not reliably namespaced in the wild — about a quarter of
well-known projects use `release/`, the rest write `release-1.37`, `2.0.x` or
`r2.15` — but that costs nothing here, because a wait naming a specific branch
is its own filter either way. That case is reported rather than left to fail quietly, since
some repositories are unreasonable: `aws/aws-sdk-go-v2` carries 82,000 tags,
one per service release, and GitHub times out serving deep pages of a
connection that size.

```console
$ roz sync github
aws/aws-sdk-go-v2 tag: 500 recorded on the first poll
aws/aws-sdk-go-v2: read the newest 500 of 82234 refs; older ones were not reached
polled 0, 0 changed, 2 ref queries
```

That is a statement about history, not a complaint about the expression. Refs
created from then on arrive at the top of the feed and are seen, so a wait for
something that has not happened yet — nearly every wait — is unaffected. What
is not covered is a wait for a ref that *already exists* and is older than the
first read reached.

A repository simply having a long history is not a problem to fix, so nothing
is put in the queue about it.

Both the bound and the branch ordering are deliberate for now and worth
revisiting; [#129](https://github.com/scottlaird/roz/issues/129) records what
is thin about them.

```console
$ roz sync github
cli/cli tag v2.98.0 appeared
NA1 closed: wait_ref
polled 0, 0 changed, 1 closed, 1 ref queries
```

**A pre-release does not satisfy a wait for the release.** `>=2.98` is not
answered by `v2.98.0-rc1`, by the constraint's own rule rather than by anything
roz invents. Ask for one explicitly when that is the point:

```console
$ roz action add --title "Test against the next RC" --verb wait_ref \
    --ref-repo cli/cli --ref '>=2.98.0-0'
```

### Waiting for the next release, without naming it

Most of the time what you mean is *"the next one"*, and looking up which number
that will be is work the repository can do for you. A version can be counted
from wherever it has got to:

```console
$ roz action add --title "Ship the migration in the next minor" --verb wait_ref \
    --ref-repo cli/cli --ref '>=minor+1'
NA2
NA2 waits for cli/cli tag >=2.98.0 (>=minor+1)
```

The version it came out as is said back, because roz worked it out rather than
you: `>=minor+1` is how it was written, `>=2.98.0` is what it means today.

**All three components count**, and the offset is how many on. With the
repository at `v2.97.1`:

| written | waits for | reads as |
| --- | --- | --- |
| `>=major+1` | `>=3.0.0` | the next major |
| `>=minor+1` | `>=2.98.0` | the next minor |
| `>=minor+2` | `>=2.99.0` | two minor lines on, the usual "not the release being cut now" |
| `>=patch+1` | `>=2.97.2` | the next point release |

Everything below the bumped component is zeroed, so `minor+2` against `v2.97.1`
is `2.99.0` rather than `2.99.1` — a release is the whole of its line, and the
patch you happened to be on says nothing about where the next line starts. The
result is `>=` rather than `=`, so a line that opens at `2.99.1` because
`2.99.0` was pulled still satisfies it.

**The version is worked out once and then fixed.** Re-deriving it on each poll
would move its own goalposts: every release that shipped would push the target
out by one and the gate would never open. It is worked out from the highest
release already tagged in the series, counting pre-releases as not having
happened.

That means it needs the repository's releases to have been read, which the
first sync after you write it does — asking for a gate is itself what makes
roz start reading those tags:

```console
$ roz action wait-ref --action NA3 --ref-repo acme/api --ref '>=major+1'
NA3 waits for acme/api tag >=major+1, once its releases have been read
$ roz sync github
acme/api tag: 47 recorded on the first poll
NA3 waits for >=4.0.0 (>=major+1)
```

The same expressions are what a pipeline step takes, where relative is the only
form allowed — see [Defining a pipeline](#defining-a-pipeline). Here both work,
because a person writing one action does know which release they mean often
enough for `>=2.98` to be worth having.

**A rule that parses as neither is refused.** `>=minr+1` is not a version
constraint and not a gate, and reading it as a literal tag name — which is what
an unrecognised expression otherwise means — produces an action that waits for
ever for a tag nothing is called:

```console
$ roz action add --title "Wait" --verb wait_ref --ref-repo cli/cli --ref '>=minr+1'
Error: "minr" is not a version component: use major, minor, patch
```

### Several release series in one repository

A monorepo tags `v1.2.3` and, disjointly, `api/v3.4.5`. A path prefix picks the
series:

```console
$ roz action add --title "Wait for the next s3 release" --verb wait_ref \
    --ref-repo aws/aws-sdk-go-v2 --ref 'service/s3/>=1.107'
```

**Series never compare across.** `service/s3/>=1.107` is answered only by a tag
under `service/s3/`, and a wait with no prefix means a *top-level* tag — never
`api/v2.3.4`, however the numbers fall. Two series that share a repository are
unrelated, and their version numbers mean nothing to each other.

The prefix is also what GitHub is asked to filter on, so a repository with
thousands of tags across a dozen components returns only the one in question.

### Waiting for something that is not a version

An expression that is not a constraint is a literal name or a glob, which is
how to wait for a branch being cut:

```console
$ roz action add --title "Port the fix once 1.5 is branched" --verb wait_ref \
    --ref-repo acme/api --ref-kind branch --ref 'release-1.5'
```

The two forms cannot be confused: `release-1.5` does not parse as a constraint,
and `>=1.5` is not a plausible branch name.

### Why this is worth more than it looks

Where a project tags a release only once the previous one has finished rolling
out, *"wait for the next release"* means *"the previous release is fully
deployed"* without observing any deployment at all. The approximation can only
fire **late**, never early, which is the harmless direction for a gate.

`wait_ref` has no `wait_days`. A release date is not yours to influence and
there is nobody to chase, so an overdue report would be noise — and since an
overdue wait now puts an item in the queue, it would be durable noise.

`ref_contains` — "is this pull request in that release" — is
[#124](https://github.com/scottlaird/roz/issues/124) and deliberately not here.
The obvious implementation is an ancestry check, and it is wrong: a change
cherry-picked onto a release branch has a different SHA there, so ancestry
answers "not present" for anything that reached a release the way patch
releases are usually built.

## Upgrading while something is running

`serve`, `syncer`, `watch` and `mcp` read the schema once, at startup. If you
rebuild the binary and run `roz init` from another terminal, they stop:

```console
$ roz serve
serving http://127.0.0.1:8737/
Error: schema: the database migrated while this was running: now migration 10 (10 applied), was migration 9 (9 applied); restart to pick up the new schema
```

They exit rather than reload, because a restart is cheap and a process serving
a schema it does not understand is not. Migration `0008` dropped five columns
from `project`, which is the shape that breaks a server left running from the
morning — and it would have surfaced as whichever query ran first, not as this.

## Backing it up

`VACUUM INTO`, not `cp`. In WAL mode the committed data is split between the
database and the `-wal` beside it, so copying the file alone can catch a torn
state — which works most of the time, the worst property a backup can have.
This is consistent even while `roz serve` is writing.

```console
$ roz db backup
/home/scott/.local/share/roz/backups/roz-20260811T065748Z.db

$ roz db backup
/home/scott/.local/share/roz/backups/roz-20260811T065749Z.db
```

The default is a timestamped file in `backups/` beside the database.
Timestamped because SQLite refuses to write over a file that is already there,
so any fixed name would work once and then start failing. `--out` puts it
somewhere else and is refused the same way — nothing here takes a `--force`,
since a backup that can quietly replace another is not one.

Restoring is deliberately harder than backing up. The file is opened and
checked first, so something that is not a roz database, or is ahead of this
build, is refused before anything is touched:

```console
$ roz db restore ~/notes.txt
Error: reading /home/scott/notes.txt: opening /home/scott/notes.txt: file is not a database (26)
```

An existing database is left alone unless you say otherwise, and even then it
is renamed rather than deleted — the database being replaced may be the reason
for the restore, and a plain rename works on one SQLite cannot open:

```console
$ roz db restore ~/.local/share/roz/backups/roz-20260811T065748Z.db
Error: /home/scott/.local/share/roz/roz.db already exists; pass --replace to move it aside and restore over it

$ roz db restore ~/.local/share/roz/backups/roz-20260811T065748Z.db --replace
moved the previous database to /home/scott/.local/share/roz/roz.db.replaced-20260811T065803Z
restored /home/scott/.local/share/roz/roz.db from /home/scott/.local/share/roz/backups/roz-20260811T065748Z.db
```

Stop anything holding the database first. SQLite has no way to tell a running
process that its file was replaced underneath it, so a `roz serve` left
running would carry on reading the database that is no longer there — unlike a
migration, which [it does notice](#upgrading-while-something-is-running).

## The logo

`assets/roz.svg` is the master; `assets/roz-mark.svg` is the same drawing with
the chain dropped, the frame thickened and the head replaced by a filled tile,
because at 16px the beads are noise and a hairline frame smears into a grey
band. Everything else in `assets/` is generated from those two.

The page carries the 32px icon inline as a data URI rather than linking a
file. The page is one file by design — `roz render` writes it to disk as
readily as `roz serve` serves it — and an icon fetched over a second request
is one more thing that can fail, or simply not be there when the file is
opened from disk.

There is no rasteriser with an alpha channel on a stock macOS box, so
`assets/unmatte.py` renders each size twice through Quick Look, once over
white and once over black, and recovers the alpha:

    over white:  Cw = C*a + (1-a)
    over black:  Cb = C*a
    so           a  = 1 - (Cw - Cb)  and  C = Cb / a

Standard library only, about three seconds for the whole set. Regenerate with:

```console
$ python3 assets/unmatte.py assets/roz-mark.svg 32 assets/roz-32.png
```

## Where to read more

- [Issues](https://github.com/scottlaird/roz/issues) — what is left.
- [`TODO.md`](TODO.md) — what is settled and why, and what is deliberately out
  of scope. Design decisions, not a work list.
- [`internal/schema/README.md`](internal/schema/README.md) — the schema, the
  migration rules, and what SQLite will not let you do.
- [`internal/store/README.md`](internal/store/README.md) — entities, field
  kinds, and how to add one.
