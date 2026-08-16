---
name: roz
description: Use when working with a roz work queue — reading what to do next, recording pull requests and decisions, or reacting to roz's event log. Covers the two-process arrangement an agent needs, the actor rule, and which closes belong to roz rather than to you.
---

# Working with roz

roz keeps a work queue correct mechanically. Projects are what a person plans
from; actions are what gets read. Every mutation is an event in an append-only
log.

The division worth internalising: **roz does the mechanical tracking, you do the
judgement.** roz knows a pull request merged and closes the action for it,
correctly and for free. It does not know whether a project still matters. Spend
your attention on the second kind of thing and let roz have the first — the
alternative is doing roz's job approximately, per token, and disagreeing with
the status page.

## The identifiers are not always ROZ1 and NA1

`ROZ` and `NA` are only the defaults. The prefixes are chosen at `roz init` —
`--project-prefix`, `--action-prefix` — and after that they are fixed for the
life of the database, so a queue you are handed may well call its projects
`API1` and its actions `TODO1`. Do not hardcode the defaults, and do not guess
from one identifier you saw in passing.

Ask the database. Every sequence-numbered entity stores its prefix as the
`kind` column, so any listing will tell you:

```console
$ roz project list --fields id,kind,n -o json
[{"id":"API1","kind":"API","n":1}]
```

`roz action list --fields id,kind,n` is the same question for actions. Over MCP
the output format is the server's, so the answer arrives as the `KIND` column of
a table rather than as JSON; `--fields` works the same either way. (An empty
database has no row to read the prefix from — but it also has nothing to name
yet, so the first thing you create answers it.)

Worth getting right because these identifiers are the shared vocabulary: they
are what the person says to you and what you say back, and `API12` only removes
ambiguity if both ends agree what `API` is.

## The arrangement

Two processes, and you want both:

- **`roz mcp`** serves every command over MCP on stdin and stdout. This is how
  you read the queue and record decisions.
- **`roz watch -o json`** is a long-running monitor. This is how you find out
  something changed.

The monitor has to be a real process. Over MCP, `watch` is forced to `--once`,
which is a bounded read and not a stream — an agent that only has the MCP
surface has to remember to ask again, which is polling with extra steps. If
nobody has set a monitor up for you, say so rather than looping on
`watch --once`.

## Every write records who made it

**Over MCP this is already handled.** The server sets the actor itself, from
what the client calls itself at startup (falling back to `roz mcp --agent
<name>`, default `mcp`), and it is always an `agent:` prefix — there is no way
to write as a person from there, which is the point. `actor` is not an argument
the tools accept: passing one is an error, not an override. The same goes for
`db` and `output`, which the server has already decided.

**The flag is for the CLI.** When you shell out instead — because you want to
follow the log, or because you have no MCP server — pass `--actor agent:<name>`
on every command that changes anything: `project
add|set|close|supersede|snooze|wake|link-issue`, `action
add|set|close|snooze|wake|add-blocker|hide-behind|link-pr`, `repo track|set`,
`pr track`, `note`, `exception`.

Nothing enforces it there. The default is `human`, so a write that forgets the
flag makes the log claim the person did it, and the log is append-only — it
stays wrong.

Reads (`list`, `show`, `watch`, `verb list`) take no actor.

### The two surfaces are the same commands

The MCP tools are generated from the CLI's own command tree, so the mapping is
one-to-one and mechanical: `roz action add` is `action_add`, `roz pr announce`
is `pr_announce`, `roz view list` is `view_list`. Flags become arguments of the
same name. The validation, the cascade on close, and the wording of an error
are the same code in both directions — so anything you learn from `roz
<command> --help` applies to the tool, and anything documented here applies to
both unless it says otherwise.

Five commands are not exposed: `init` and `db`, which decide where state lives,
and `serve`, `syncer` and `mcp` itself, which never return. `watch` is exposed
but forced to `--once` — see above.

Two writes deliberately refuse the flag. `roz pr announce` and `roz issue
observe` write *observed* columns — a person hand-entering what a sync would
have seen — so they set their own actor rather than taking yours:
`sync:slack-manual` for the announcement, and `sync:jira-manual` or
`sync:github-issue-manual` depending on which tracker the issue came from.
Passing `--actor` to either is an unknown-flag error, not an override. That is
deliberate: the exception is one named verb instead of a hole in the rule.

## Closes that are not yours

Verbs come in two kinds. `roz verb list --fields verb,closes,predicate_key`
says which is which:

- **`closes: human`** — `write`, `review`, `decide`, `file`, `investigate`,
  `run`, `announce`. Somebody did the thing and says so. Fair game.
- **`closes: predicate`** — `merge` (`pr_merged`), `wait_review`
  (`pr_approved`), `address_comments` (`pr_threads_clear`), `rebase`,
  `undraft`, `send_for_review`, `wait_ref`, `wait_issue`, and the rest. These
  close themselves when the fact becomes true, with `predicate` as the actor.

**Do not hand-close a predicate action.** `roz action close NA7` will not stop
you — no check refuses it — and the result is a judgement recorded where there
was going to be a fact, an action closed before the thing actually happened, and
a log that no longer explains itself. If a `merge` action is still open and the
PR looks merged, the answer is a sync, not a close.

The same rule read from the schema: columns are **authored** (a person or an
agent wrote them) or **observed** (sync wrote them). A sync can never overwrite
a judgement and a judgement can never invent a fact. The store enforces that
half; the verb half is on you.

## The queue is a query that already exists

```
roz action list --unblocked --sort priority
```

That is the answer to "what now" — ready, not hidden, not waiting on anyone, in
the order the status page uses. Do not re-derive it from `pr list`: it is
slower, it costs tokens, and it will disagree with what the person is looking
at.

Related listings worth knowing: `roz action list --waiting` is what the queue
folds away and who it is folded away behind, `--expired` is snoozed past a date
that has passed, and `roz project list --orphaned` is live work with no open
action and no snooze — which is how a project goes quiet without anyone
noticing.

Saved views are named questions — `roz view list` first, then
`roz action list --view open_actions`. If you find yourself writing the same
`--filter` twice, that is a view worth saving (`roz view add`), which is a row
in a table rather than a code change.

`--filter` takes a CEL expression over the listing's columns. `--explain-filter`
says how much of it ran in SQL.

## Reacting to the stream

Each line of `roz watch -o json` is one event:

```json
{"seq":2,"at":"2026-08-16T03:10:35.891Z","actor":"agent:claude","kind":"created",
 "subject_type":"action","subject_id":"NA1","field":"","old_value":"","new_value":"",
 "severity":"info","correlation":"fb6e659e-…"}
```

- **Ignore your own writes.** Your `--actor agent:<name>` lines come back down
  the stream, and an agent that reacts to them will react to its own reaction.
  `roz watch --exclude-actor agent:<name>` drops them at the source, which is
  better than remembering to skip them: the rule holds for the backlog and the
  tail alike, and it cannot be forgotten halfway through a session.
- **`correlation` groups one act.** Closing an action instantiates a pipeline,
  frees what it blocked, and unhides what sat behind it — all under one
  correlation id. Treat those as one thing that happened, not five.
- **`severity: exception`** is what a monitor is for. `info` is ordinary
  traffic.
- **`predicate` in `actor`** means roz closed something because a fact became
  true. That is usually the interesting one: it is the queue moving without
  anybody touching it.

`--kind`, `--severity`, `--since` and `--exclude-actor` all narrow the stream at
the source, which is cheaper than filtering in your own head. `--exclude-actor`
is repeatable and takes a comma-separated list, so a session running two agents
can hide both.

roz polls SQLite internally, because SQLite cannot push a notification. That is
one indexed query on `seq`, once, regardless of how many readers there are — so
do not add a second layer of polling on top of it.

## Sync spends somebody else's budget

`roz sync github` costs GitHub rate limit, and `roz serve` (or `roz syncer`) is
already syncing on a timer. Read what sync has already written rather than
asking for a fresh one. If something looks stale, check whether a syncer is
running before reaching for `sync`.

## Where you are actually useful

The authored half of the schema is the shape of the answer:

- writing the `why` on an action, so the next reader knows what it is for
- setting and arguing about priority, effort, and what a project is blocked on
- noticing a project has gone quiet, or that work has drifted from its stated aim
- splitting a lump of work into projects and actions
- telling roz about pull requests as they are opened (`roz pr track`), so the
  mechanical tracking has something to track

## Things not to do

- **Do not touch the database file.** Never move, copy or delete `roz.db` or its
  `-wal` while `serve`, `syncer`, `watch` or `mcp` hold it open. Use `roz db
  backup`.
- **Do not start or restart the person's long-running processes.** `serve`,
  `syncer`, `watch` and `mcp` are theirs. Say a restart is needed and leave it.
- **Do not write to GitHub or Jira as roz.** roz reads trackers and never writes
  to one. If a Jira ticket needs closing, that is your own tooling doing it —
  roz is what told you the PR merged.
