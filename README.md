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
| `todo config show` / `set` | The settings the database carries: the Jira host, the project keys worth linking, and whose queue this is. |
| **projects** | |
| `todo project add` | Allocate a project and print its id. |
| `todo project show` | Print one project in full. |
| `todo project list` | The projects table, in creation order or `--sort priority`; with `--orphaned` and `--expired`. |
| `todo project set` | Change authored columns. |
| `todo project snooze` | Defer a project to a real date. |
| `todo project wake` | Clear a snooze. |
| `todo project supersede` | Record that one project is the same work as another. |
| `todo project close` | Close it, dropping whatever was still open on it. |
| `todo project jira` | Record by hand what Jira says about an issue — summary, status, sprint, assignee. Stands in for Jira sync. |
| `todo project link-jira` / `unlink-jira` | Say which issues a project tracks. More than one is allowed. |
| **jira** | |
| `todo jira show` / `list` | Issues as last observed, and which projects track them. |
| **actions** | |
| `todo action add` | Allocate an action and print its id. |
| `todo action show` | Print one action, with what blocks it, what it blocks, and its pull requests. `-o json` carries the same. |
| `todo action list` | Actions in creation order, or `--sort priority`; with `--unblocked`, `--waiting`, `--stale`, `--open`, `--expired` and filters. |
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
| `todo pr track` | Start tracking a pull request, keyed `owner/repo#number`. `--pipeline` if this one reaches merge differently from its repository. |
| `todo pr set` | Change that pipeline, or clear it. The only authored column a pull request has. |
| `todo pr show` / `list` | Read tracked pull requests. |
| `todo pr announce` | Record by hand that it was announced in Slack. Stands in for Slack sync, and closes the `send_for_review` step waiting on it. |
| `todo sync github` | Refresh observed columns from GitHub, and close the steps GitHub has finished. Read-only against GitHub. |
| `todo syncer` | The same on a loop, backing off as rate limit heads down. |
| `todo mcp` | Serve the commands over MCP on stdio, for an agent. Writes are recorded as `agent:<client>`. |
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
| `todo render` | Regenerate the status page: calendar, queue, what is merely waiting, and the projects table. Prose fields render as Markdown, and GitHub and Jira identifiers become links wherever they are written; Jira needs `todo config set`. |
| `todo verify` | Record that a project or action was checked against reality. Feeds `--sort staleness`. |

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

```console
$ todo config set --owner scott --jira-base-url https://example.atlassian.net/browse --jira-prefix CDSS
config owner: "" → "scott"
config jira_base_url: "" → "https://example.atlassian.net/browse"
config jira_prefixes: "[]" → "[\"CDSS\"]"
```

These are properties of the queue rather than of one command, so they live in
the database. See [Settings](#settings).

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
NA3 closed: scottlaird/todo#39 is send_for_review
  NA4 is now ready
```

The last two lines are the point. The fact that closes the step has just
arrived, so the step closes now rather than at the next poll — the same settle
pass `todo sync` runs. It means a command that reads as "write down what I did
in Slack" also closes actions and instantiates what follows them, which is why
it prints what it closed.

Jira is the same arrangement, keyed on the issue rather than the project,
because an integration would have `CDSS-1744` and not `TD1` — and because an
issue is a record in its own right, so one nothing tracks is still stored:

```console
$ todo project jira CDSS-1744 --status "In Progress" --sprint "Sprint 42" --assignee scott
CDSS-1744 status: "" → "In Progress"
CDSS-1744 sprint: "" → "Sprint 42"
CDSS-1744 assignee: "" → "scott"
CDSS-1744 synced_at: "" → "2026-08-10T14:47:22.053Z"
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
$ todo config show
created_at     2026-08-11T02:09:46.064Z
id             config
jira_base_url  https://example.atlassian.net/browse
jira_prefixes  ["CDSS"]
owner          scott
updated_at     2026-08-11T02:09:46.088Z
```

They are **columns, not a key-value bag**, which is the same reasoning the
rest of the schema follows. A settings change then gets the diff-based event
log for free — `todo watch` shows the page starting to link somewhere new —
plus CHECK constraints and real types, where a string→string table gets none
of that and invites `enable_foo = "true"`. The cost is a migration per
setting, which for a handful of settings is the right trade.

`--jira-prefix` replaces the whole list rather than adding to it, so there is
a way to remove one; pass it once per key, or `--jira-prefix ""` to link none.
Keys are upper-cased on the way in, and a base URL with no scheme is refused
rather than repaired:

```console
$ todo config set --jira-base-url example.atlassian.net
Error: jira base URL "example.atlassian.net" needs an http or https scheme, e.g. https://example.atlassian.net/browse
```

Nothing here is guessed at. Without a base URL and at least one prefix, Jira
keys render as plain text, because the shape of a key is not distinctive —
`UTF-8`, `SHA-256` and `CVE-2024-1234` all match it, and a link that goes
confidently to the wrong place is worse than no link.

`--db` stays a flag, since it says which database to open. `TODO_JIRA_BASE_URL`
and `TODO_JIRA_PREFIXES` override the stored values for a single run.

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
todo project set SL10 --priority 1
```

**Actions** sort on six terms, each breaking ties in the one before:

| # | term | lives on | set with |
|---|---|---|---|
| 1 | `rank_pin` | `action` | `todo action set NA7 --rank-pin 1` (`0` clears it) |
| 2 | `priority` | the `project` it advances | `todo project set SL10 --priority 1` |
| 3 | `rank_class` | the `actionverb` it uses | choose the verb; `todo verb list` shows the classes |
| 4 | unblocks count | computed from `action_blocks` | `todo action add-blocker --from NA8 --to NA7` raises NA7's, since `--from` is the blocked one |
| 5 | `effort` | the `project` it advances | `todo project set SL10 --effort hours` |
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

## Staleness

`updated_at` says when something last *changed*. It cannot say when someone
last *looked at it and was satisfied*, and those are different questions: an
item nobody has touched for a month is fine if it was reviewed on Friday and
alarming if it was not.

`todo verify` records the second one, on projects and actions:

```console
$ todo verify TD1
TD1 verified at 2026-08-11T04:45:50.628Z
$ todo project list --sort staleness
ID   STATUS  PRI  EFFORT  SNOOZED UNTIL  TITLE
TD3  active  -    -       -              never checked
TD1  active  -    -       -              checked last week
TD2  active  -    -       -              checked yesterday
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
deciding what it should say, which is why the design sketch lists `todo verify`
alongside `todo sync` as the only writers of observed fields. Like
`todo pr announce`, it is a separate command rather than an `--actor`
override — one named verb instead of a hole in the rule — and it is logged as
`sync:verify`, so the log says a person went and looked rather than that
something reported it.

## Prose fields

Some fields are written as sentences rather than as values, and those are
Markdown: `action --why`, `project --summary`, both `--snooze-reason`s, and a
calendar note. A title is *not* — it is a name, so asterisks in one stay
asterisks.

```console
$ todo action set NA7 --why 'unblocks the *split*, once `todo sync github` runs'
NA7 why: "" → "unblocks the *split*, once `todo sync github` runs"
```

The database keeps what you typed. `show -o json` returns the source, the
event log records the source, and only `todo render` turns it into HTML — so
nothing is lost if you decide later that a field should have been plain.

On the page, an action's `--why` sits under its title in the queue, and a
project's `--summary` gets a row of its own beneath the project, spanning the
table. A summary is prose and does not fit a cell.

Identifiers are linked wherever they appear, including inside a sentence:
`CDSS-1557` and `scottlaird/todo#54` both become links, while a bare `#54`
does not, because which repository it means is a guess. Linking happens on the
parsed document rather than on the text, so an identifier inside a code span
or inside a link you wrote yourself is left alone.

The one thing rejected on input is raw HTML:

```console
$ todo action set NA7 --why 'see <b>this</b>'
Error: action.why: raw HTML is not allowed here: "<b>" — write it as Markdown, or wrap it in backticks to show it literally
```

Refusing it here rather than stripping it at render time means you find out
straight away, instead of wondering later why half a sentence is missing from
the page.

## For an agent

`todo mcp` serves the same commands over the Model Context Protocol, on stdin
and stdout, for an agent to call without shelling out.

**The tools are the commands.** They are derived from the command tree rather
than written out again, so the two cannot drift: the name is the command path
with an underscore (`action add` → `action_add`), the description is that
command's own help, and the arguments are its flags and whatever its usage
line names. Forty-six of them:

| | |
|---|---|
| settings | `config_show` `config_set` |
| projects | `project_add` `project_show` `project_list` `project_set` `project_snooze` `project_wake` `project_supersede` `project_close` `project_jira` `project_link-jira` `project_unlink-jira` |
| actions | `action_add` `action_show` `action_list` `action_set` `action_snooze` `action_wake` `action_add-blocker` `action_hide-behind` `action_link-pr` `action_close` |
| GitHub | `repo_track` `repo_show` `repo_list` `repo_set` `pr_track` `pr_set` `pr_show` `pr_list` `pr_announce` `sync` |
| the log | `note` `exception` `watch` (bounded to one read) |
| jira | `jira_show` `jira_list` |
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
// ← TD1
```

## Checks

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
$ todo pr track scottlaird/todo#1 --pipeline direct
scottlaird/todo#1
$ todo pr list
ID                 STATE  DRAFT  REVIEW  MERGE  CHECKS  FROZEN  PIPELINE  TITLE
scottlaird/todo#1  -      -      -       -      -       no      direct    -
scottlaird/todo#2  -      -      -       -      -       no      -         -
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
$ todo pr set scottlaird/todo#1 --pipeline ""
scottlaird/todo#1 pipeline: "direct" → ""
```

Changing it affects the chain the next close instantiates. Actions already
created are left alone — they exist, and something may already be waiting on
them.

## Checks

GitHub reports a conclusion per check context. Those are rows in `pr_check`,
one per check, rather than a single JSON column — and the difference is what
the log says when one moves:

```
2026-08-11T05:01:00.000Z  info  sync:github  changed  scottlaird/todo#67  checks/test: "SUCCESS" → "FAILURE"
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

## Upgrading while something is running

`serve`, `syncer`, `watch` and `mcp` read the schema once, at startup. If you
rebuild the binary and run `todo init` from another terminal, they stop:

```console
$ todo serve
serving http://127.0.0.1:8737/
Error: schema: the database migrated while this was running: now migration 10 (10 applied), was migration 9 (9 applied); restart to pick up the new schema
```

They exit rather than reload, because a restart is cheap and a process serving
a schema it does not understand is not. Migration `0008` dropped five columns
from `project`, which is the shape that breaks a server left running from the
morning — and it would have surfaced as whichever query ran first, not as this.

## Where to read more

- [`TODO.md`](TODO.md) — what is left, what is settled, and what is
  deliberately out of scope.
- [`internal/schema/README.md`](internal/schema/README.md) — the schema, the
  migration rules, and what SQLite will not let you do.
- [`internal/store/README.md`](internal/store/README.md) — entities, field
  kinds, and how to add one.
