# TODO

What is left, taken from the design sketch plus what has come up since. Items
are ordered roughly by what blocks what, not by importance.

## Where things stand

Built: the schema and its migration machinery; the diff-and-emit layer; six
entities — `project`, `action`, `pr`, `github_repo`, `calendar_window` and
`actionverb`; GitHub sync, one-shot and as a polling loop; the predicate
registry the verb vocabulary resolves against; and the pipelines a repository
chooses between. `todo watch` tails the log.

One command is still a stub that exits 1: `verify`.

The queue is mechanical, which was the claim the whole design rested on.
Closing a `write` action instantiates the repository's pipeline as a chain of
blocked actions; sync then closes each step as GitHub satisfies its predicate,
and each closure frees the next. Nobody types "the pull request merged".

## The critical path

In dependency order. Nothing later can be finished first.

- [ ] **The rest of the queue queries.** `--unblocked` and `--expired` exist.
      Left: `--stale`, which catches an item claiming done whose pull request
      is still open, and `--waiting`.
- [ ] **A real `todo render`.** What exists is three `<pre>` blocks and no
      design worth the name. Templates, the ranking, and the status page the
      whole thing exists to regenerate are still ahead.

## What dogfooding needs

Nothing blocking. Track the repo, track a pull request, `action add --verb
write`, `action close --pr`, and let `todo syncer` close the steps as GitHub
finishes them. What would make it pleasant rather than merely possible:

- [ ] A `todo pr announce` habit, since `send_for_review` closes on the
      announcement and GitHub cannot supply it.
- [ ] Settling after `pr announce` as well as after sync, so the step closes
      when the fact arrives rather than at the next poll.

`todo render` and the web server are not needed for it. `todo db backup` is
not either, but a real database makes it worth having sooner.

## Entities the sketch specifies but nothing uses

The tables exist and migrate; there is no Go entity and no command for any of
them.

- [ ] `priority` and `priority_target` — the dated priorities block. Authored,
      superseded rather than edited.
- [ ] `review_rule` — routing policy. The sketch says implement it late.

## Smaller gaps

- [ ] `todo verify` — still stubbed, though no longer for want of a design:
      `todo pr announce` set the pattern. See the open question.
- [ ] Ranking — `rank_class`, then `unblocks_count`, then `effort`. The page
      now sorts by `rank_pin`, then the priority of the project an action
      advances, then creation order, which is a first step and not the sketch's
      ranking: `rank_class` is on every verb already and `unblocks_count`
      needs the dependency graph. `action list` and `project list` default to
      creation order and take `--sort priority` for the same ordering.
- [ ] `action show -o json` omits the edges, which the table shows. They are
      not columns of `action`, and a record marshals from its own columns.
- [ ] `--db` is a package-level variable in `internal/cli`, written by flag
      parsing. Harmless for one-shot commands and for `serve`, which opens the
      store once before any service starts, but it means two commands cannot
      safely run in one process — a puzzling test flake in waiting. Thread the
      flag through instead.
- [ ] `todo db backup` and `todo db restore` — thin wrappers over SQLite, so
      the syntax does not have to be remembered. `VACUUM INTO` is the backup:
      it is consistent against a live database, which a file copy is not.
- [ ] Nothing consumes `todo watch`. Sync raises a `pr_unresolvable` exception
      when a tracked pull request goes invisible, and today only a human
      watching would see it.
- [ ] Sync polls whatever is tracked, one pull request at a time by hand. A
      per-repository "poll everything of mine" would want a `search` query and
      a rule for when a pull request stops being tracked.
- [ ] **`todo pr track --pipeline`.** A pull request takes its repository's
      pipeline, and there is no way to say this one is different — a
      hotfix that skips review, or a change to protected code that needs more
      than the usual chain. The column would sit on `pr` and fall back to the
      repository's when unset, which is the same shape `github_repo.pipeline`
      already has against the default.
- [ ] **Pipelines with more than one review step.** A real change can need
      reviewing by your own team, then by the owners of code it happens to
      touch, then by whoever guards the protected parts — three reviews, in
      order, by different groups. Today `wait_review` is one step closing on
      one `reviewDecision`, which cannot express any of that.

      This is a design and schema question before it is code. A step would
      need to name *who* it waits for, the predicate would need to ask whether
      that group has approved rather than whether the pull request has, and
      GitHub's `reviewDecision` is a single verdict that will not answer it —
      it wants the individual reviews and the teams they came from.

      **Blocked on the CODEOWNERS inference below**, which is where knowing
      which teams a pull request needs comes from. Designing this first would
      mean guessing at the shape of what feeds it.
- [ ] **Infer reviewers from CODEOWNERS.** Which teams a pull request needs
      is derivable: the files it touches, matched against the repository's
      CODEOWNERS. Nothing here reads either yet — sync asks for the pull
      request's state, not its file list.

      It is worth doing for its own sake, since "who is this waiting for" is
      most of what makes a `wait_review` action readable. Three other things
      want it: pipelines with more than one review step, which is blocked on
      it; `review_rule`, which the sketch describes as routing policy; and the
      `review` verb, which cannot close on a predicate while nothing knows
      whose review was wanted.

      Ownership can be per-directory and can change under a long-lived pull
      request, so what is inferred is an observation with a time, not a fact
      about the repository.
- [ ] **Sort by staleness — what has gone longest without being looked at.**
      Half of it exists. `project.last_verified_at` is in the schema and
      `todo verify` is the verb designed to stamp it; both are waiting on each
      other. What is missing is the same column on `action`, a way to mark one
      reviewed, and the ordering itself.

      Two clocks, and they answer different questions: the last time an item
      *changed* is already recoverable from `updated_at` and the log, while
      the last time someone *looked at it and was satisfied* is not recorded
      anywhere. Staleness is the second one — an item nobody has changed for a
      month is fine if it was reviewed on Friday, and alarming if it was not.
- [ ] **Push events into an agent's session, as an MCP channel.** The MCP
      server answers when asked; nothing reaches an agent between turns. A
      Claude Code *channel* is the mechanism for that — a server declaring
      `experimental: {"claude/channel": {}}` and emitting
      `notifications/claude/channel` has its events injected into the session,
      and the agent reacts. The contract is a capability key and a
      notification, so this server can do it without the Node SDK the
      documentation's examples use.

      Worth knowing before building it: the transport is not where the delay
      is. A merge reaches the queue when the syncer next polls, so the
      interval dominates anything the notification path costs, and the lever
      for a faster round trip is that interval or a GitHub webhook. Channels
      are a research preview and need
      `--dangerously-load-development-channels`, so this buys reach into
      sessions nobody is watching rather than speed.
- [ ] **Track GitHub issues, not only Jira.** The `jira_*` columns on
      `project` name one tracker in the schema, in the entity, and in `todo
      project jira`. Home projects use GitHub issues, and the reader for them
      already exists — `gh api graphql` and the batching that polls pull
      requests. The refactor is generalising the columns to a tracker and a
      key, which is a table rebuild and a decision: one set of columns with a
      `tracker` discriminator, or a child table so a project can carry both.
      Worth doing before there is much data to migrate.
- [ ] Tracking a pull request assigned to us rather than authored by us has
      nowhere to record *why* it is tracked. That is a schema change, and it is
      what `review` needs before it can close on a predicate.

## Open questions

**`todo verify` writes an observed column.** It stamps `last_verified_at`, so a
human running it is exactly what `Tx.Update` refuses. `todo pr announce` has
since shown the shape that works: a named verb that picks its own sync actor,
with a distinct actor name so the log does not claim an integration said it.
The same approach fits here. It is a decision rather than a design problem
now.

**Sync cadence versus event fidelity.** The sketch's own question, mostly
answered: the log records transitions rather than poll results, so a quiet
poll writes nothing. What remains open is whether a minute is often enough to
catch states that do not persist — `UNSTABLE` and `BEHIND` in particular.

**`--json` can reach columns that have a dedicated verb.** `project set --json
'{"superseded_by":"TD94"}'` skips the target-existence check that `project
supersede` performs. The foreign key still catches a bad target, so the cost is
a rawer error. Worth deciding whether `ApplyJSON` should refuse such columns.

## Settled, recorded so it is not relitigated

- Identifier prefixes live in the database, chosen at init, write-once.
- Pull request keys are `owner/repo#123`; a repository must be tracked first.
- `github_repo.pipeline` is authored, not observed — reading branch protection
  needs admin, and it is a statement about how someone works.
- Migrations are the only thing executed; `schema.sql` is documentation with a
  test keeping it honest.
- **Which migrations have run is recorded in `applied_migration`, not inferred
  from `user_version`.** A high-water mark silently skips a migration numbered
  below one already applied, which is what two branches adding migrations
  produces. Numbers need not be higher than everything already merged.
- Events come from automatic field-level diffs, never hand-written calls.
- `set`, not `edit` — `edit` reads as interactive.
- No `DELETE` guard on `sequence`. If someone really wants to edit the
  database, they are going to edit the database.
- GitHub is read through `gh api graphql`, batched with aliases. Measured: 100
  pull requests cost 4 points of 5000 an hour, and beyond about 150 the API
  returns an opaque 502. REST is not an option — `reviewDecision` and
  `mergeStateStatus` exist only in GraphQL.
- `last_synced_at` is `auto`: written when something else changes, never
  logged.
- Absence is not a fact. Where GitHub reports nothing, sync leaves the stored
  value alone rather than clearing it.
- **Absence is not completion either.** Every predicate is false where nothing
  has been observed, so an unsynced pull request closes nothing — and an
  unreachable GitHub cannot empty the queue.
- **Settling is not sync, and has its own actor.** Recording that a pull
  request merged is an observation, written as `sync:github`; deciding the
  merge action is therefore done carries out a rule someone wrote into the
  vocabulary, and is written as `predicate`. The actor that observes a fact is
  exactly the one not allowed to act on it.
- **Only completing something makes it done.** `superseded`, `dropped` and
  `obsolete` all leave an action in state `dropped`, and abandoning work does
  not instantiate the pipeline that finishing it would have. Both were wrong
  until `project close` needed them.
- **Closing a project drops its open actions**, as `obsolete` rather than
  `dropped`: the reason is that the project went away, not that anyone decided
  against the action. Their cascades still run, so nothing is left waiting on
  something that will never move.
- **A satisfied step closes whatever its state.** A merged pull request means
  the merge happened, whatever the chain expected to come first. Reality
  outranks the plan.
- Hand-entered observations get their own actor — `sync:slack-manual` and
  `sync:jira-manual` — so the log never claims an integration reported
  something typed in. The actor is fixed by the command rather than taken from
  `--actor`, which keeps the exception to one named verb instead of a hole in
  the rule.
- **Jira observations are keyed on the issue, not the project.** An
  integration has `CDSS-1744`, not `TD106`. Every project carrying the key
  gets the observation, and a key nobody carries is reported rather than
  refused: this tracks a subset of what Jira holds.
- A verb naming a predicate the build lacks is refused when the store opens.
  Retired verbs are skipped; rows are deactivated, never deleted.
- `review` is seeded human-closed, against the sketch, until there is somewhere
  to record that a pull request is tracked because it is assigned to us.
- **A closed blocker stops blocking, and no edge is ever removed.** The
  blocked/ready state is recomputed from the open blockers in one place, so
  adding an edge and closing one cannot disagree. A snooze outranks both: it
  is a decision about time.
- **A pipeline is instantiated as a chain of blocked actions, all at once.**
  Closing a step frees the next through the same unblocking every other action
  gets, so there is no separate notion of advancing a pipeline to keep
  correct.
- **Which verb opens a pipeline is a column, not an `if`.** `actionverb`
  carries `starts_pipeline`; having a subject pull request is not on its own a
  reason, since investigating one ends when you know the answer.
- **Closing allocates identifiers between reading and writing.** The plan is
  read in one transaction, the identifiers allocated with none open, and the
  cascade written in a second. Allocation writes on its own connection, so a
  transaction holding the write lock would deadlock against it.
- **Blocking cycles are refused, not just self-edges.** The `CHECK` catches
  `A → A`; a recursive query catches the rest. A cycle is a set of actions
  that never unblocks.
- **The page is the one place that does not print in creation order.** An
  action has no priority of its own, so it inherits the priority of the
  project it advances; `rank_pin` overrides that, because overriding is what
  it is for. Anything unprioritised sorts last — unstated is not the same as
  low, but it has to go somewhere, and behind the stated ones does no harm.
- **The page reloads itself through server-sent events, not a websocket.**
  The page only ever listens, the browser reconnects on its own, and it is a
  few lines of the standard library against a dependency and a handshake. The
  change signal is the log's head, since nothing changes here without an
  event.
- **The MCP tools are the CLI's commands, derived from the cobra tree.** A
  flag added to a command is a tool argument next time the server starts, and
  calling a tool runs that command, so validation, the actor rule and the
  cascade are the same code rather than a second implementation. What cannot
  be derived is which commands make sense to an agent, which is a list.
- **`watch` is exposed to MCP bounded, not excluded.** Following would never
  return, so the tool is the read-once form with the filters left to the
  caller: an agent asks what has happened since a sequence number, and asks
  again. `--once` and `--interval` are the server's, not arguments.
- **An MCP write is `agent:<client>`**, taken from what the client calls
  itself at initialize and falling back to `--agent`. `--actor` is not offered
  as a tool argument, so there is no way to write as a person from there.
- **One renderer, used twice.** `todo serve` calls the same function `todo
  render` writes to a file, per request. Two ways of building the page would
  eventually be two different pages.
- **The server listens on loopback and has no authentication.** It is a
  personal queue; making it reachable should take a deliberate act.
- Calendar kinds are free text; capacity is not. Nothing branches on kind,
  while the sort reads capacity.
- **What a verb instantiates is a named pipeline, not a `review_policy` enum.**
  `action_pipeline` holds them and `pipeline_step` their verbs, so a chain is
  rows rather than a branch in code. A repository takes the lowest-numbered
  active pipeline when it is tracked, resolved once rather than read through a
  NULL later.
- **Two pipelines, not one with everything skipped.** `review` is `undraft →
  send_for_review → wait_review → merge`; `direct` is `undraft → merge`.
  `wait_review` closes on an approval a repository needing no review will
  never receive, so it has to be absent rather than satisfied. Skipping is for
  steps already true when the pipeline is instantiated — `undraft`, where pull
  requests are not created as drafts.
- An identifier is allocated only after the record validates, so a mistyped
  verb costs no number. Allocation still writes on its own connection, which
  means no read transaction may be open across it — SQLite answers the upgrade
  with `SQLITE_BUSY_SNAPSHOT` rather than waiting.

## Deliberately out of scope

From the sketch, and still true:

- Anything that **writes** to GitHub, Jira or Slack. Read-only in both
  directions keeps the blast radius at "the list is wrong".
- Inferring priority or intent. The system mechanises transitions that are
  already determinate; it does not decide what matters.
- Holding design notes. Pointers only, behind `design_refs`.
- Replacing the weekly review. Fewer dropped items, not fewer decisions.
- Design-meeting topics as entities. A topic comes off the list by being
  discussed, not by being done, and an action that cannot close on completing
  anything is what makes a queue noisy. If it ever becomes an entity, the
  distinguishing field is `resolved_by: discussion` — which is also the test
  for whether it belongs.

Jira and Slack sync remain out for scheduling reasons rather than design ones.
`todo pr announce` and `todo project jira` cover both by hand, which is enough
to work with and enough to know what the real sync has to produce.
Without it, `send_for_review` cannot close on its own — that verb is the only
predicate GitHub cannot satisfy.
