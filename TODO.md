# TODO

What is left, taken from the design sketch plus what has come up since. Items
are ordered roughly by what blocks what, not by importance.

## Where things stand

Built: the schema and its migration machinery, the diff-and-emit layer, and
three entities — `project`, `pr` and `github_repo`. `todo watch` tails the log.
Eleven commands are still stubs that exit 1.

The sketch's core claim — that the queue is mechanical — is not testable yet,
because `action` does not exist.

## The critical path

Everything below is in dependency order. Nothing later can be finished first.

- [ ] **GitHub sync.** `todo sync github`, shelling out to `gh api graphql`,
      writing observed columns as `sync:github`. The schema was written against
      GraphQL's vocabulary (`APPROVED`, `CLEAN | BLOCKED | BEHIND`), not REST's.
      Needs: which pull requests to poll (see open questions), the `checks`
      JSON shape, and `stacked_on` computed from `base_ref` against
      `github_repo.default_branch`.
- [ ] **Predicate registry.** `predicate_key` → `func(pr) bool` in code. The
      sketch is explicit that a key with no registered function must fail
      loudly at startup rather than silently at 3am.
- [ ] **Seed `actionverb`.** The vocabulary table. Cannot be seeded before the
      registry exists, or the `CHECK` tying `closes = 'predicate'` to a
      `predicate_key` points at nothing.
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
- [ ] Nothing consumes `todo watch` yet. The sketch assumes a monitor tailing
      the log surfaces exceptions; today a human has to be looking.

## Open questions

**`todo verify` writes an observed column.** It stamps `last_verified_at`, so a
human running it is exactly what `Tx.Update` refuses. The sketch files `verify`
under *observe — the only writers of observed fields*, treating verification as
an observation. Options: a narrow `Tx.Verify` that is exempt by construction,
or retag the column as authored. Deferred until sync exists and observed fields
start moving for real.

**Which pull requests does sync poll?** `todo pr track` names them one at a
time, which does not scale. The alternative is discovery per tracked
repository — "my open pull requests in these repos" — which changes what
`github_repo` is for and needs a rule for when a pull request stops being
tracked.

**`send_for_review` cannot auto-close without Slack.** Its predicate is
`announced_at IS NOT NULL`, and the sketch calls the Slack announcement the one
input GitHub cannot supply. With Slack out of scope, that verb is human-closed
in practice, and the sketch's showcase flow stalls at step 5.

**What does `review_policy = none` change?** Closing a `write` action is
supposed to instantiate `send_for_review → wait_review → merge`. For a
repository needing no review that pipeline is wrong, but the replacement is
undecided — probably just `merge`, possibly `undraft → merge`. This is the
question `github_repo` was added to make answerable, and it wants settling
before the cascade is written.

**Sync cadence versus event fidelity.** The sketch's own open question. Polling
gives current state and loses transitions between polls. Minute-by-minute is
probably enough *provided the log records observed transitions rather than poll
results*, or a monitor sees noise on every cycle.

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

Jira and Slack sync are out for now as a scheduling matter rather than a design
one; GitHub is in.
