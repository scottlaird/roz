<img src="assets/roz-512.png" alt="" width="256">

*Always watching.* -- [Roz](https://www.youtube.com/watch?v=mm8R3u_b0yU&t=23s).

# roz

Roz is a a personal todo-list tool for managing Github-backed
development projects.  It tracks projects, actions, issues, and pull
requests for you.  It can help shepherd PRs through complex review
processes, letting you know when items need your attention without
polling.  This lets you concentrate on getting work done and not on
tracking PR status and dependencies.

Roz is a single-binary CLI tool that runs on MacOS and Linux (and
presumably also Windows, although this is untested).  All data and
configuration lives in a SQLite database.  All access to Github goes
through Github's `gh` CLI tool, so roz doesn't need its own Github
credentials or configuration.

## Roz's approach

Roz primarily tracks *projects*, *actions*, and *PRs*:

- A project is a concrete goal that needs to be accomplished.
  Projects can be nested inside of other projects and can have
  dependencies on other projects and actions.
- An action is a specific thing that needs to be done.
- PRs are Github Pull Requests.

Projects lay out a goal, while actions are specific steps that need to
be taken to move the project(s) forward.  Roz focuses specifically on
the *next action* that you need to take to make progress.  This is
somewhat inspired by
[GTD](https://en.wikipedia.org/wiki/Getting_Things_Done).

Roz's driving goal is to always be able to answer "what is the next
action that I should work on?", or at least "what are the
highest-priority next actions that aren't blocked?"

Each action has a specific *verb* associated with it.  "Write",
"Investigate", "Test", and so forth.  Many actions are human-managed,
but roz also has support for a number of verbs with automated
*predicates* that can close themselves when specific events occur.
For instance, a `merge` action will block until a specific PR is
merged.

Roz gives each project and each action its own ID.  By default,
project IDs start with `ROZ` followed by a number, and action IDs
start with `NA` followed by a number.  You can change these prefixes,
*but only when the database is first created*.

Roz supports *pipelines*, which are collections of actions that are
instantiated automatically for PRs.  For instance, it comes with a
"review" pipeline that stacks `undraft`, `send_for_review`,
`wait_review`, and `merge` verbs, each with their own automated
predicates.  Adding a PR with this pipeline will create a stack of
actions that require PRs to be undrafted first, then explicitly sent
for review, followed by waiting for PR approval, and finally merging.

You can view roz's list of projects and actions via the CLI (`roz
action list --unblocked`), via roz's (optional) web UI, or you can
have an AI agent query roz via MCP.

Roz doesn't require an AI agent to be useful, but it was originally
created to work around shortcomings in my interactions with Claude.
Today's AI tools are impressively good at a number of things, but
*consistently* doing mechanical projects and housekeeping for a
reasonable cost is not one of them.  So I'm using roz to keep track of
projects and near-future actions, and then mostly having Claude
interact with roz for me.  I tell Claude to create projects and
actions for me, and then I provide rough guidance on prioritization,
and my todo list mostly manages itself for a day or two at a time.  I
make tweaks a couple times per week, and then do weekly reviews to
make sure that priorities are right.  So far, that's been good enough,
and I'm able to be much more productive with less stress.

## YMMV

I've found that *for me*, having more than 2 or 3 things in-flight and
tracked mentally usually just means that I'm stressed out and starting
to drop things.  This means that I avoid starting new things, even
when blocked.  This is because the mental effort of tracking (and
losing) items can dominate the preceived effort involved in getting
actual work done.  Writing projects and actions down helps a lot, but
I've never found a system that actually meshes well with what my
working process really looks like.

With roz, I can *mostly* ignore the mechanical actions that clog up
general-purpose todo lists.  Roz will tell me if a PR needs my
attention, which Github itself is surprisingly bad at, and then Claude
can `monitor` roz's notification stream and suggest actions (or even
take them itself) for things that aren't trivially automatable.

## Installing Roz

You'll need a recent Go compiler installed, plus the the
[`gh`](https://cli.github.com) CLI for GitHub reads.  To install, just
run:


```
go install github.com/scottlaird/roz@latest
```

That will download all of the dependencies (including newer versions
of the Go compiler toolchain, if required), compile `roz` and copy it
into Go's binary directory, usually `~/go/bin`.

## Features

There are a lot of things hiding in roz that may not be obvious at
first glance.  A non-exhaustive list:

- It knows how to poll Github's API and back off when overloaded.
- It understands `CODEOWNERS` files and Github's group API, and it
  understands that the groups listed in CODEOWNERS may overlap.  It is
  able to take existing approvals and see which files remain
  un-approved, and then identify which additional groups are needed.
- It allows you to create filters using [CEL](https://cel.dev/), so
  you can flexibly filter queries in ways that make sense to your
  workload.
- You can save filters and sort orders in the database and use them in
  Roz's web UI (in some cases at least).
- Roz's web UI supports Markdown in "prose" fields.
- Roz's web UI will auto-link (and tooltip) any reference to known
  project or action IDs, as well as Github PRs, Github issues, or Jira
  tickets.
- Each Github repo can have its own default review settings and PR
  pipeline, plus a customized short name so you don't need to see
  `examplecorp/frontend-ui-library#1234` all the time when
  `uilib#1234` would work for you.
- Roz understands `git` tagging and branching, as well as semantic
  release numbers, so you can say things like "this cleanup PR is
  blocked until 2 new minor releases have been created".

---

## Walkthrough

Meet Alice.

Alice is a software developer at ExampleCorp.  ExampleCorp builds
software on GitHub, tracks issues in GitHub Issues or Jira, and asks
for code review in per-team Slack channels. Its code is spread across
several repositories.  Most repos have `CODEOWNERS` files that dictate
which team(s) can approve changes to specific files or
directories. Alice works there, has four pull requests in flight on a
good day, and would like to stop having to keep a map of all of her
PRs in her head at all times.

### Alice tracks a pull request through review

She starts a database, tells roz about a repository, and names the work:

```console
$ roz init
initialised /Users/alice/.local/share/roz/roz.db (schema 36, action=NA, project=ROZ)

$ roz repo track example/server --short-name server
example/server

$ roz project add --title "Rate-limit the public API" --priority 2 --effort days
ROZ1
```

Roz gives each action and project's IDs a prefix.  By default, actions
start with `NA` (for *next action*) and projects start with `ROZ`.
You will be seeing lot of these, so it's worth considering what naming
scheme you want to use.  Once the database has been created, these
can't easily be changed.

```console
$ roz init --project-prefix API --action-prefix TODO
initialised /Users/alice/.local/share/roz/roz.db (schema 36, action=TODO, project=API)
```

It's possible to edit the database to change these, but they tend to
leak into commit messages, in chat, and in whatever an agent has
written down.  The advantage of this is that it gives every project
and action a distinct, stable, non-ambiguous name.  `API12` refers to
one thing and can be said to an agent without context.  So try to pick
something short and unambiguous that you can live with.

<br>

Alice then tracks the pull request and says she has written it:

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

### What roz reads from GitHub

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
example/server#812 announce_count: "0" → "1"
NA3 closed: example/server#812 is send_for_review
```

She still posts the message herself — roz writes down that it happened, works
out where it should go, and closes the step. Which channel is a lookup on the
team being asked, because a change touching storage should reach the storage
channel whichever repository it is in.

Announcing it again three days later is how she chases a review that has
stalled, and that restarts the clock roz measures the wait against: asking a
second time is a decision to accept more waiting, and a pull request that goes
overdue the instant it is pinged trains her to ignore the mechanism. The count
is what keeps that honest — a fourth telling is not a first one, and sometimes
a ping is the last thing before escalating.

roz does not have chat integration yet; at some point it may gain the ability
to listen to Slack review requests on its own. What it will not do is *infer*
the announcement from GitHub state, because that would make the one failure
this catches invisible again — the pull request that is technically fine and
that nobody has been asked to look at.

### Alice wants a web interface to view the status of all of her projects

```console
$ roz serve
serving http://127.0.0.1:8737/
```

Roz will serve up a web interface on the listed URL, showing the
top-priority actions, a list of waiting actions, your live projects,
and a graph of dependencies.  `roz serve` also runs a Github syncer
process in another thread, and writes `roz watch` output to stdout.

### Alice wants an agent to do the slightly less-mechanical parts of the job

She runs an agent for code work and would rather not spend its attention — or
its tokens — on remembering which pull request is where.

```console
$ roz mcp
```

That serves every command over MCP on stdin and stdout, so an agent
can read the queue and record decisions without dealing with a shell.
Optionally, `roz serve --mcp` can offer MCP over HTTP on localhost if
this works better for you.

Alice can then ask her agent to run `roz watch` and monitor its
output.  This will let Claude see when events happen without it
needing to poll.  Roz will handle the mechanical side of PRs, and the
agent can deal with exceptions like PR comments, test failures, and
PRs getting kicked out of merge queues.

That turns the log into a trigger. Alice can have the agent tell roz
about every pull request she opens and help her keep her projects in
priority order, and the same stream lets it act without being asked:
notice her working patterns, attach a Jira ticket (or Github issues)
to a PR that has none, close the ticket once the PR merges.

Each field in roz's database schema is either "authored" -- written by
a person or an agent, or "observed", which is information that roz
collects mechanically from Github or other data sources.  The
background sync process that fetches data from Github can't overwrite
"authored" fields, and agents can't overwrite "observed" fields.

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

[skills/roz/SKILL.md](skills/roz/SKILL.md) is a skill that tells an
agent how to work this way roz.  It explains the `authored`/`observed`
split, the tools, how syncing works, what belongs to roz rather than
to the agent, and what to do with the stream of `watch`
notifications. 

### Alice needs two teams to approve a PR

`example/server#812` touches code owned by more than one team. Alice
usually wants to ask her own team for review first, and only send
reviews to other teams for approval if her teamate's review isn't sufficient.

```console
$ roz repo prefer example/server --prefer @example/backend,@example/serverinfra
example/server prefers @example/backend → @example/serverinfra

$ roz codeowners --pr example/server#812
ask, in order
  1. @example/backend      9 files  preferred, and covers outstanding files
  2. @example/serverinfra  3 files  preferred, and covers outstanding files
```

Roz's review preferences are treated as advisory; it will skip teams that can't meaningfully move PRs closer to final approval.  If `@example/backend`'s approval covers all files, then there's no need to bother `@example/serverinfra`.

### Alice wants a list of all her open PRs across every repo

```console
$ roz pr list --filter 'state == "OPEN"' --fields id,title,review_decision,unresolved_threads
ID                    TITLE                       REVIEW             UNRESOLVED THREADS
example/frontend#233  Show the rate-limit banner  REVIEW_REQUIRED    1
example/server#804    Retry the upstream fetch    CHANGES_REQUESTED  3
example/server#812    Rate-limit the public API   REVIEW_REQUIRED    0
```

`--filter` takes a [CEL](https://cel.dev) expression over whatever columns the
listing has, and is pushed into SQL where it provably means the same thing
there.

### Alice wants to know which open PRs are blocked on comments

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

### Alice has a cross-repo dependency

`example/frontend#233` cannot merge until `example/server#812` has merged *and*
a new server release has been cut.

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

## Roz's model

Four things, and the rest is detail.

**Projects** (`ROZ1`) are what you plan from. They have a priority, an effort,
optionally a parent, and links to tracker issues.

**Actions** (`NA5`) are what you do. Each has a **verb**, and the verb
decides how it closes:

- a **predicate** verb closes when a fact becomes true — `merge` closes when
  GitHub says merged, `wait_review` when the decision is `APPROVED`
- a **human** verb closes when you say so — `write`, `decide`, `investigate`

**Pipelines** are the shape work takes in a repository: closing a `write` step
lays out `undraft → send_for_review → wait_review → merge`. Different repos can have different default pipelines, and the pipeline can be changed for each PR if needed.

**The log** shows every change, derived by diffing records rather than
written by hand — so the history cannot drift from the data. It is
append-only.

And the rule underneath all of it: **authored columns are people's,
observed columns are sync's,** and neither can write the other's.

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
| **GitHub** | `roz repo track`/`show`/`list`/`set`/`prefer` · `roz pr track`/`show`/`list`/`set`/`announce`/`chain` · `roz sync github` · `roz codeowners` · `roz ref list` |
| **issues** | `roz issue show`/`list`/`observe` |
| **views** | `roz view add`/`show`/`list`/`drop` |
| **reading** | `roz serve` · `roz watch` · `roz note` · `roz exception` · `roz verify` |
| **vocabulary** | `roz verb list`/`set` · `roz pipeline add`/`show`/`list`/`set`/`retire` |
| **agents** | `roz mcp` · `roz owner set`/`list` · `roz page set`/`show`/`list`/`clear` |

## Where to read more

- **[skills/roz/SKILL.md](skills/roz/SKILL.md)** — what an agent should know
  before touching a roz queue. Copy or symlink it into `~/.claude/skills/roz/`.
- **[NOTES.md](NOTES.md)** — Claude spamming itself with design
  decisions and corner cases.
- `roz <command> --help` for flags, which are documented at the command rather
  than in a table that would drift from them.

## Status

roz is still a work in progress.  It's in daily use by its author, but
it's far from complete, and there are a number of half-implemented
features sitting in [issues](https://github.com/scottlaird/roz/issues)
(above).  Roz is designed to be flexible enough to support different
working styles, but a lot of the customation code doesn't actually
exist yet.  Please file issues when you run into oversights or things
that you want to do that Roz can't currently accomplish.

