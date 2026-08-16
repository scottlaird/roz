# roz

<img src="assets/roz-256.png" alt="" width="128" align="right">

**A work queue that stays correct without being maintained.**

If you have more than about three pull requests open at once, you are keeping a
queue in your head: which one is waiting on a review, which one is waiting on
*you*, which one has been sitting for a week because a review came back and you
have not looked yet, and which one is only blocked because something in another
repository has not shipped.

That queue rots. Not because the work changes, but because the mechanical half
of it does — a pull request merges, a check goes red, an approval arrives — and
nothing updates the list you are carrying. A list you have to maintain by hand
is a list you stop trusting, and a list you do not trust is one you re-derive
every morning by opening twelve browser tabs.

roz keeps the mechanical half for you. It reads GitHub, and when something it
was waiting for becomes true, the item closes itself and whatever it was
blocking becomes live. What is left in the queue is the part that needed a
person.

```
go install github.com/scottlaird/roz@latest
```

Go, SQLite (no cgo), and the [`gh`](https://cli.github.com) CLI for GitHub
reads. Nothing is written to GitHub, ever. The database is a single file.

---

## Meet Alice

ExampleCorp builds software on GitHub, tracks issues in GitHub Issues or Jira,
and asks for code review in per-team Slack channels. Its code is spread across
several repositories, and CODEOWNERS decides which team can approve which
parts. Alice works there, has four pull requests in flight on a good day, and
would like to stop being the thing that remembers what is blocked on what.

Everything below is Alice's, and every command is real.

## Alice tracks a pull request through review

She starts a database, tells roz about a repository, and names the work:

```console
$ roz init
initialised /Users/alice/.local/share/roz/roz.db (schema 36, action=NA, project=ROZ)

$ roz repo track example/server --short-name server
example/server

$ roz project add --title "Rate-limit the public API" --priority 2 --effort days
ROZ1
```

Then she tracks the pull request and says she has written it:

```console
$ roz pr track example/server#812
example/server#812

$ roz action add --title "Write the limiter" --verb write \
    --project ROZ1 --pr example/server#812
NA1

$ roz action close NA1
NA1 done (completed)
  created NA2 un-draft example/server#812
  created NA3 send for review example/server#812 (blocked by NA2)
  created NA4 wait for review example/server#812 (blocked by NA3)
  created NA5 merge example/server#812 (blocked by NA4)
```

Closing the `write` step laid out the rest of the path. Alice typed one line;
roz knows the shape a pull request takes at ExampleCorp because that shape is a
*pipeline*, and it is data rather than code.

Only the first step is in her queue. The others are blocked behind it, which is
the point — a queue showing four things she cannot do yet is a queue she learns
to skim:

```console
$ roz action list --unblocked
ID   STATE  VERB     PROJECT  SNOOZED UNTIL  LATE  TITLE
NA2  ready  undraft  ROZ1     -              -     un-draft example/server#812
```

Now `roz sync github` reads the pull request. When GitHub says it is no longer
a draft, `NA2` closes itself and `NA3` becomes live. When the review decision
turns `APPROVED`, `NA4` closes. Nobody types anything, and the queue is right
at every step.

**That is the whole idea.** The rest of this is detail.

## What roz reads from GitHub

Sync is read-only and batched into one GraphQL query. It records what GitHub
says: state, draftness, the review decision, the merge state, unresolved review
threads, the checks rollup, who was asked to review.

```console
$ roz sync github
example/server#812 state: "" → "OPEN"
example/server#812 review_decision: "" → "REVIEW_REQUIRED"
example/server#812 merge_state_status: "" → "CLEAN"
polled 1, 1 changed
```

There is one signal it deliberately does not infer: **whether anybody was
actually told to review it.** At ExampleCorp that happens in a Slack channel,
and GitHub cannot see it. A pull request can sit open, un-draft and unblocked
for three days because it was never posted anywhere a reviewer looks — and from
GitHub's side that is indistinguishable from a review that is merely slow.

So `send_for_review` is a step of its own, and it closes when Alice says the
announcement happened. She tells roz where her team is reached once:

```console
$ roz owner set @example/backend --channel '#server-reviews'
@example/backend → #server-reviews

$ roz pr announce example/server#812
example/server#812 → #server-reviews (@example/backend, the owner this is waiting for)
example/server#812 announced_at: "" → "2026-08-16T02:41:30.559Z"
example/server#812 announced_channel: "" → "#server-reviews"
NA3 closed: example/server#812 is send_for_review
```

She still posts the message herself — roz writes down that it happened, works
out where it should go, and closes the step. Which channel is a lookup on the
team being asked, because a change touching storage should reach the storage
channel whichever repository it is in.

roz does not have chat integration yet; at some point it may gain the ability
to listen to Slack review requests on its own. What it will not do is *infer*
the announcement from GitHub state, because that would make the one failure
this catches invisible again — the pull request that is technically fine and
that nobody has been asked to look at.

## Alice wants a web interface to view the status of all of her projects

```console
$ roz serve
serving http://127.0.0.1:8737/
```

A page with the queue, what is merely waiting, and the live projects. It syncs
in the background on a timer, tails the change log to the terminal, and
reloads itself when anything moves — so it can be left open on a second
monitor and read rather than refreshed.

Every project and action has its own page too (`/project/ROZ1`,
`/action/NA5`), and identifiers in prose link to them.

## Alice wants an agent to do the mechanical part

She runs an agent for code work and would rather not spend its attention — or
its tokens — on remembering which pull request is where.

```console
$ roz mcp
```

That serves every command over MCP on stdin and stdout, so an agent can read
the queue and record decisions without shelling out. `roz watch` gives it the
change log as a stream.

The division that makes this safe is in the schema. Columns are either
**authored** — written by a person or an agent — or **observed**, written only
by sync. The store enforces it: **a sync can never overwrite a judgement, and a
judgement can never invent a fact.** So an agent can maintain the queue all day
without being able to claim GitHub said something it did not.

Every write records who made it. An agent passes `--actor agent:claude`, and
the log says so for ever:

```console
$ roz watch --once -n 2
2026-08-16T02:41:30.562Z  info       predicate      changed    NA4  state: "blocked" → "ready"
2026-08-16T02:31:47.453Z  info       agent:claude   created    ROZ2
```

`predicate` is roz closing something because a fact became true; `agent:claude`
is an agent; `human` is you.

The useful arrangement is the agent doing the bookkeeping and the high-level
prioritising, and roz doing the mechanical tracking — because roz does it
correctly and for free, where an agent does it approximately and per token.

There is not yet a skill telling an agent how to work this way; writing one is
[#211](https://github.com/scottlaird/roz/issues/211).

## Alice needs two teams to approve

`example/server#812` touches code owned by more than one team. Alice would
rather ask her own team first, and only widen if they cannot cover it.

```console
$ roz repo prefer example/server --prefer @example/backend,@example/serverinfra
example/server prefers @example/backend → @example/serverinfra

$ roz codeowners --pr example/server#812
ask, in order
  1. @example/backend      9 files  preferred, and covers outstanding files
  2. @example/serverinfra  3 files  preferred, and covers outstanding files
```

A preference is checked rather than trusted: a team that owns nothing in *this*
change is skipped rather than asked, so a hint cannot quietly become a habit
nobody revisits. Ask the first team, and re-running the command shows what is
still outstanding once their approval lands.

## Alice wants a list of all her open PRs across every repo

```console
$ roz pr list --filter 'state == "OPEN"' --fields id,title,review_decision,unresolved_threads
ID                    TITLE                       REVIEW             UNRESOLVED THREADS
example/frontend#233  Show the rate-limit banner  REVIEW_REQUIRED    1
example/server#804    Retry the upstream fetch    CHANGES_REQUESTED  3
example/server#812    Rate-limit the public API   REVIEW_REQUIRED    0
```

`--filter` takes an expression over whatever columns the listing has, and is
pushed into SQL where it provably means the same thing there.

## Alice wants to know which open PRs are blocked on comments

```console
$ roz pr list --filter 'unresolved_threads > 0' --fields id,title,unresolved_threads
ID                    TITLE                       UNRESOLVED THREADS
example/frontend#233  Show the rate-limit banner  1
example/server#804    Retry the upstream fetch    3
```

A question worth asking twice is worth naming:

```console
$ roz view add blocked_on_comments --entity pr \
    --filter 'unresolved_threads > 0' \
    --description 'Open pull requests with review threads nobody has cleared'
blocked_on_comments: pr, where unresolved_threads > 0

$ roz pr list --view blocked_on_comments --fields id,title
ID                    TITLE
example/frontend#233  Show the rate-limit banner
example/server#804    Retry the upstream fetch
```

Views are rows in a table, so a new question costs no code and no release.

## Alice has a cross-repo dependency

`example/frontend#233` cannot merge until `example/server#812` has merged *and*
a new server release has been cut. That is the case most likely to be forgotten
at exactly the wrong moment, because nothing in either repository knows about
the other.

```console
$ roz action add --title "merge example/frontend#233" --verb merge \
    --project ROZ2 --pr example/frontend#233
NA6

$ roz action add --title "wait for the server release" --verb wait_ref \
    --ref-repo example/server --ref '>=minor+1'
NA7
NA7 waits for example/server tag >=minor+1, once its releases have been read

$ roz action add-blocker --from NA6 --to NA5
NA6 is blocked, waiting on NA5

$ roz action add-blocker --from NA6 --to NA7
NA6 is blocked, waiting on NA7
```

`>=minor+1` is *the next minor release*, counted from wherever the repository
has got to — because when the block is written, nobody knows whether it will be
`v1.5.0` or `v1.6.0`. `NA6` stays out of the queue until both clear, and
arrives in it the poll after the release is tagged.

---

## The model, in one screen

Four things, and the rest is detail.

**Projects** (`ROZ1`) are what you plan from. They have a priority, an effort,
optionally a parent, and links to tracker issues.

**Actions** (`NA5`) are what you read. Each has a **verb**, and the verb decides
how it closes:

- a **predicate** verb closes when a fact becomes true — `merge` closes when
  GitHub says merged, `wait_review` when the decision is `APPROVED`
- a **human** verb closes when you say so — `write`, `decide`, `investigate`

Only human verbs are thinking work, which is why the queue is short.

**Pipelines** are the shape work takes in a repository: closing a `write` step
lays out `undraft → send_for_review → wait_review → merge`. They are rows, so a
repository that reviews differently says so without a code change.

**The log** is every change, derived by diffing records rather than written by
hand — so the history cannot drift from the data. It is append-only.

And the rule underneath all of it: **authored columns are people's, observed
columns are sync's,** and neither can write the other's.

## Cookbook

```console
# What should I do next?
roz action list --unblocked --sort priority

# What am I waiting on someone else for?
roz action list --waiting

# What went quiet? (live projects with nothing open against them)
roz project list --orphaned

# What has been waiting too long?
roz action list --filter 'state == "snoozed" && snooze_until < now'

# What did I finish this week?
roz pr list --since 2026-08-10 --sort -merged_at
roz issue list --since 2026-08-10

# Refresh from GitHub, and close whatever GitHub finished
roz sync github

# Defer something to a real date, rather than to a vague intention
roz action snooze NA9 --snooze-until 2026-09-01 --snooze-reason "after the freeze"

# Why is this the way it is?
roz action show NA5
roz project show ROZ1
```

Any listing takes `--fields`, `--sort`, `--filter`, `-o json|csv` and `--view`.
`roz <command> --help` is written to be read.

## Commands

The database is `--db`, or `$ROZ_DB`, defaulting to a per-platform user data
directory.

| | |
|---|---|
| **setting up** | `roz init` · `roz config show`/`set` · `roz db backup`/`restore` |
| **planning** | `roz project add`/`show`/`list`/`set`/`snooze`/`wake`/`close`/`supersede` · `roz project block`/`unblock` · `roz project link-issue`/`unlink-issue` |
| **doing** | `roz action add`/`show`/`list`/`set`/`snooze`/`wake`/`close` · `roz action add-blocker`/`hide-behind`/`link-pr` · `roz action wait-ref`/`wait-issue` |
| **GitHub** | `roz repo track`/`show`/`list`/`set`/`prefer` · `roz pr track`/`show`/`list`/`set`/`announce` · `roz sync github` · `roz codeowners` · `roz ref list` |
| **issues** | `roz issue show`/`list`/`observe` |
| **views** | `roz view add`/`show`/`list`/`drop` |
| **reading** | `roz serve` · `roz watch` · `roz note` · `roz exception` · `roz verify` |
| **vocabulary** | `roz verb list`/`set` · `roz pipeline add`/`show`/`list`/`set`/`retire` |
| **agents** | `roz mcp` · `roz owner set`/`list` · `roz page set`/`show`/`list`/`clear` |

## Where to read more

- **[NOTES.md](NOTES.md)** — the detailed record: how each piece works and, more
  usefully, *why*. Why a ranking is not a column, why an announcement is the one
  signal roz will not infer, why a predicate answers false when it has read
  nothing, what went wrong before each rule existed. It is long and it is
  greppable.
- **[TODO.md](TODO.md)** — what is open and has no issue yet.
- `roz <command> --help` for flags, which are documented at the command rather
  than in a table that would drift from them.

## Status

roz is one person's tool, used daily, on a database that has been migrated
thirty-six times without losing anything. It is not packaged, has no releases,
and its interface changes when a better shape turns up. If that suits you, it
is `go install` away.
