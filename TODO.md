# TODO

What is left, taken from the design sketch plus what has come up since. Items
are ordered roughly by what blocks what, not by importance.

## Where things stand

Built: the schema and its migration machinery, the diff-and-emit layer, three
entities — `project`, `pr` and `github_repo` — and GitHub sync, both one-shot
and as a polling loop. `todo watch` tails the log. Ten commands are still
stubs that exit 1, and all ten are `action`, `render` or `verify`.

The sketch's core claim — that the queue is mechanical — is still not
testable, because `action` does not exist. Everything below it now does.

## The critical path

In dependency order. Nothing later can be finished first.

- [ ] **Predicate registry.** `predicate_key` → `func(pr) bool` in code. Every
      predicate but one reads a `pr` column that sync now populates, so this is
      unblocked. The sketch is explicit that a key with no registered function
      must fail loudly at startup rather than silently at 3am.
- [ ] **Seed `actionverb`.** Cannot be seeded before the registry exists, or
      the `CHECK` tying `closes = 'predicate'` to a `predicate_key` points at
      nothing.
- [ ] **`action`.** Blocked on the above: `action.verb` is a foreign key into
      `actionverb`, so with foreign keys on, not one action can be inserted
      until the vocabulary is seeded.
- [ ] **`action` commands.** `add`, `close` (with the cascade), `snooze`,
      `wake`, `add-blocker`, `hide-behind`, `link-pr`, `list`.
- [ ] **The queue queries.** `--unblocked`, `--expired`, `--stale`,
      `--waiting`, `--orphaned`. `--expired` is called the highest-value query
      in the system; `--stale` is the one that catches an item claiming done
      whose pull request is still open.
- [ ] **`todo render`.** Templates, and the status page the whole thing exists
      to regenerate.
- [ ] **A web server for the rendered page**, at which point `todo serve`
      running sync, watch and the server together is the natural shape.
      `internal/service` exists for this: `service.Run(ctx, syncer, server,
      tailer)`, first failure cancels the rest.

## Entities the sketch specifies but nothing uses

The tables exist and migrate; there is no Go entity and no command for any of
them.

- [ ] `action_blocks` — the blocked-by edge.
- [ ] `action_pr` — including the `action_one_subject` partial index, which the
      sketch calls the most valuable line in the schema.
- [ ] `calendar_window` — oncall, PTO, holidays. Hand-entered at the weekly
      review. `ends_on` is inclusive and `capacity` is an enum, both
      deliberately.
- [ ] `priority` and `priority_target` — the dated priorities block. Authored,
      superseded rather than edited.
- [ ] `review_rule` — routing policy. The sketch says implement it late.

## Smaller gaps

- [ ] `todo verify` — still stubbed, and blocked on a question below.
- [ ] Action sort order — `rank_class`, then `unblocks_count`, then `effort`,
      with `rank_pin` as the override. Needs the dependency graph.
- [ ] The root `README.md` is two lines.
- [ ] Nothing consumes `todo watch` yet. Sync raises a `pr_unresolvable`
      exception when a tracked pull request goes invisible, and today only a
      human watching would see it.
- [ ] Sync polls whatever is tracked, one pull request at a time by hand. A
      per-repository "poll everything of mine" would want a `search` query and
      a rule for when a pull request stops being tracked.
- [ ] Tracking a pull request assigned to us, rather than authored by us, has
      nowhere to record *why* it is tracked. That is a schema change.

## Open questions

**What does `review_policy = none` change?** Closing a `write` action is
supposed to instantiate `send_for_review → wait_review → merge`. For a
repository needing no review that pipeline is wrong, but the replacement is
undecided — probably just `merge`, possibly `undraft → merge`. This is now
directly in the way: the predicate registry is the next thing to build, and
the cascade is where this gets encoded.

**`todo verify` writes an observed column.** It stamps `last_verified_at`, so
a human running it is exactly what `Tx.Update` refuses. The sketch files
`verify` under *observe — the only writers of observed fields*. `todo pr
announce` has since set a precedent for this shape of problem: a named verb
that picks its own sync actor, rather than an `--actor` override. The same
approach would work here, with an actor saying a person asserted it.

**Sync cadence versus event fidelity.** The sketch's own open question, now
half-answered. Polling loses transitions between polls, but the log records
observed transitions rather than poll results — a quiet poll writes nothing,
and `last_synced_at` moves without being logged. What remains open is whether
a minute is often enough to catch states that do not persist, `UNSTABLE` and
`BEHIND` in particular.

**`--json` can reach columns that have a dedicated verb.** `project set --json
'{"superseded_by":"SL94"}'` skips the target-existence check that `project
supersede` performs. The foreign key still catches a bad target, so the cost is
a rawer error. Worth deciding whether `ApplyJSON` should refuse such columns.

## Settled, recorded so it is not relitigated

- Identifier prefixes live in the database, chosen at init, write-once.
- Pull request keys are `owner/repo#123`; a repository must be tracked first.
- `review_policy` is authored, not observed — reading branch protection needs
  admin, and it is a statement about how someone works.
- Migrations are the only thing executed; `schema.sql` is documentation with a
  test keeping it honest.
- Events come from automatic field-level diffs, never hand-written calls.
- `set`, not `edit` — `edit` reads as interactive.
- No `DELETE` guard on `sequence`. If someone really wants to edit the
  database, they are going to edit the database.
- GitHub is read through `gh api graphql`, batched with aliases. Measured: 100
  pull requests cost 4 points of 5000 an hour, and beyond about 150 the API
  returns an opaque 502. REST is not an option — `reviewDecision` and
  `mergeStateStatus` exist only in GraphQL.
- `last_synced_at` is `auto`: written when something else changes, never
  logged. Logging it would bury real transitions under one event per pull
  request per poll.
- Absence is not a fact. Where GitHub reports nothing, sync leaves the stored
  value alone rather than clearing it.
- Hand-entered observations get their own actor — `sync:slack-manual` — so the
  log never claims an integration reported something typed in.

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
`todo pr announce` covers the one Slack signal anything depends on, by hand.
Without it, `send_for_review` cannot close on its own — that verb is the only
predicate GitHub cannot satisfy.
