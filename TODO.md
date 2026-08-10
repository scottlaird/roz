# TODO

What is left, taken from the design sketch plus what has come up since. Items
are ordered roughly by what blocks what, not by importance.

## Where things stand

Built: the schema and its migration machinery; the diff-and-emit layer; six
entities — `project`, `action`, `pr`, `github_repo`, `calendar_window` and
`actionverb`; GitHub sync, one-shot and as a polling loop; the predicate
registry the verb vocabulary resolves against; and the pipelines a repository
chooses between. `todo watch` tails the log.

Two commands are still stubs that exit 1: `render` and `verify`.

Closing works, and with it the cascade: closing a `write` action instantiates
the repository's pipeline as a chain of blocked actions, frees what it was
holding up, and brings back whatever was hidden behind it. The queue now moves
on its own when a human closes something. It does not yet move when GitHub
does — nothing asks the predicates during sync, which is the next thing.

## The critical path

In dependency order. Nothing later can be finished first.

- [ ] **Closing on predicates during sync.** The registry can answer "is this
      done" and the pipeline steps are actions with predicate verbs and a
      subject pull request, so everything is in place; nothing asks yet. This
      is the difference between a queue that models the work and one that
      keeps up with it.
- [ ] **The queue queries.** `--unblocked`, `--expired`, `--stale`,
      `--waiting`, `--orphaned`. `--expired` is called the highest-value query
      in the system; `--stale` catches an item claiming done whose pull request
      is still open, and needs the predicates.
- [ ] **`todo render`.** Templates, and the status page the whole thing exists
      to regenerate.
- [ ] **A web server for the rendered page**, at which point `todo serve`
      running sync, watch and the server together is the natural shape.
      `internal/service` exists for this: `service.Run(ctx, syncer, server,
      tailer)`, first failure cancels the rest.

## What dogfooding needs

Enough works to track this repository's own pull requests today: track the
repo, track a pull request, `action add --verb write`, then `action close --pr`
to get the chain. What is missing before it is not more work than it saves:

- [ ] **Sync closing the predicate steps.** Without it every `undraft`,
      `wait_review` and `merge` action has to be closed by hand, which is the
      opposite of the point. This is the one blocker.
- [ ] **`action list --unblocked`.** Otherwise the queue is read by eye,
      filtering out the blocked steps mentally.
- [ ] A `todo pr announce` habit, since `send_for_review` closes on the
      announcement and GitHub cannot supply it.

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
- [ ] Action sort order — `rank_class`, then `unblocks_count`, then `effort`,
      with `rank_pin` as the override. `rank_class` is already on every verb;
      the rest needs the dependency graph.
- [ ] `action show -o json` omits the edges, which the table shows. They are
      not columns of `action`, and a record marshals from its own columns.
- [ ] `todo db backup` and `todo db restore` — thin wrappers over SQLite, so
      the syntax does not have to be remembered. `VACUUM INTO` is the backup:
      it is consistent against a live database, which a file copy is not.
- [ ] Nothing consumes `todo watch`. Sync raises a `pr_unresolvable` exception
      when a tracked pull request goes invisible, and today only a human
      watching would see it.
- [ ] Sync polls whatever is tracked, one pull request at a time by hand. A
      per-repository "poll everything of mine" would want a `search` query and
      a rule for when a pull request stops being tracked.
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
'{"superseded_by":"SL94"}'` skips the target-existence check that `project
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
  has been observed, so an unsynced pull request closes nothing.
- Hand-entered observations get their own actor — `sync:slack-manual` and
  `sync:jira-manual` — so the log never claims an integration reported
  something typed in. The actor is fixed by the command rather than taken from
  `--actor`, which keeps the exception to one named verb instead of a hole in
  the rule.
- **Jira observations are keyed on the issue, not the project.** An
  integration has `CDSS-1744`, not `SL106`. Every project carrying the key
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
