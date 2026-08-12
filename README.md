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
| `roz issue observe` | Record by hand what a tracker says about an issue — summary, status, iteration, assignee. Stands in for tracker sync. |
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
| `roz pipeline list` | The pipelines and their steps. |
| **the log** | |
| `roz watch` | Follow the event log, or `--once` to print and exit. |
| `roz note` | Append a note to a subject's history without changing it. |
| `roz exception` | Record an exception for a monitor to surface. |
| **other** | |
| `roz calendar add` / `show` / `list` / `set` | Oncall, PTO and holidays. |
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

Sync also closes any action whose predicate the new observations satisfy; the
walkthrough gets to that below.

`roz syncer` runs the same thing on a loop, slowing down as the rate limit
budget drops and backing off on a 429.

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
`jira`, which is the only tracker anything can read; a second one is a place
in the schema and nothing more until something fills it:

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

Now the announcement is a fact, `send_for_review` is satisfied, and the next
sync notices:

```console
$ roz sync github
NA3 closed: scottlaird/roz#39 is send_for_review
  NA4 is now ready
polled 1, 0 changed, 1 closed

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

**Projects** sort on one field, `project.priority`, an integer from 1 to 4.
Unprioritised sorts last — unstated is not the same as low, but it has to go
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

`roz project unblock` removes the edge for when the dependency was wrong
rather than satisfied — saying so should not mean closing something unfinished.

A snooze outranks the graph: a project deferred to a date is not un-deferred
by an edge, because a snooze is a decision about time. There is no project
equivalent of `hide-behind`, since folding something out of a queue is a
judgement about a queue and the project table is not one.

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
