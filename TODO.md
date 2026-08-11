# TODO

What is left, taken from the design sketch plus what has come up since. Items
are ordered roughly by what blocks what, not by importance.

## Where things stand

Built: the schema and its migration machinery; the diff-and-emit layer; six
entities — `project`, `action`, `pr`, `github_repo`, `calendar_window` and
`actionverb`; GitHub sync, one-shot and as a polling loop; the predicate
registry the verb vocabulary resolves against; and the pipelines a repository
chooses between. `todo watch` tails the log.

Every command in the tree is implemented; nothing is a stub any more.

The queue is mechanical, which was the claim the whole design rested on.
Closing a `write` action instantiates the repository's pipeline as a chain of
blocked actions; sync then closes each step as GitHub satisfies its predicate,
and each closure frees the next. Nobody types "the pull request merged".

## The critical path

Empty. Everything the sketch put in dependency order is built: the entities,
the pipelines, the cascade, sync closing what GitHub finishes, the queue
queries and a page to read them on. What is left below is depth and polish,
and none of it blocks anything else.

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
- [ ] **A timeout on waiting — `okay_to_wait_until` on an action.** A
      `wait_review` or a `merge` that has been sitting long enough should ask
      for attention. [#47](https://github.com/scottlaird/todo/pull/47) makes
      this more necessary rather than less: excluding `rank_class = wait` from
      `--unblocked` was right, since four of ten queue items were waits, but it
      left the queue itself silent about them. The page has its own waiting
      block, so they are visible to anyone who goes and looks; what is missing
      is anything that speaks up when a wait has gone on too long, which is a
      page you have to remember to read away from being no signal at all.

      Shape: an authored per-action deadline, defaulting from the verb or the
      repository rather than typed every time, and an `exception` event when it
      passes. That reuses `severity = 'exception'`, which is already what
      monitors filter on, rather than inventing a second alerting path.

      It is **not** a snooze. A snooze hides something until a date; this
      reveals something after one.
- [ ] **A `git_ref` entity, and the two predicates it enables.** Waiting for a
      release is currently a snooze to a guessed date, which is wrong in both
      directions: if the release slips the item wakes early, and if it ships
      early the item sleeps through it.

      An observed row — repository, name, kind, commit, created_at — supports
      both `ref_exists`, for "the vX.Y branch was cut", and `ref_contains`, for
      "this pull request is in that release". The first is a condition in its
      own right, and it is also a cheap proxy for the second: where a release
      is tagged only once the previous one has finished rolling out, waiting
      for the next `.0` says "the previous one is deployed" without observing
      any deployment. The proxy can only fire late, never early, which is the
      harmless direction for a gate.

      Two things to get right. The argument wants to be a pattern or a lower
      bound rather than a literal name, since at the time the block is written
      nobody knows whether the next release is `v1.5.0` or `v1.5.1`. And
      ancestry cannot be `merge-base --is-ancestor` against a merge commit: a
      change cherry-picked onto a release branch has a different SHA there than
      on the default branch, so the obvious check reports "not present" for
      every patch release. That wants `git cherry` or patch-id matching.

      Predicates stay pure over the database — the work is the observation and
      its sync, not the predicate.
## Open questions

**Sync cadence versus event fidelity.** The sketch's own question, mostly
answered: the log records transitions rather than poll results, so a quiet
poll writes nothing. What remains open is whether a minute is often enough to
catch states that do not persist — `UNSTABLE` and `BEHIND` in particular.

**`--json` can reach columns that have a dedicated verb.** `project set --json
'{"superseded_by":"TD94"}'` skips the target-existence check that `project
supersede` performs. The foreign key still catches a bad target, so the cost is
a rawer error. Worth deciding whether `ApplyJSON` should refuse such columns.

## Settled, recorded so it is not relitigated

- **Prose fields are Markdown; titles are not.** `action.why`,
  `project.summary`, the two `snooze_reason`s and a calendar note carry
  `format:"markdown"`. The store keeps the source, `show -o json` returns the
  source and the log records the source; only the page renders. A title is a
  name, so `the *old* pipeline` keeps its asterisks there. `jira_issue.summary`
  is observed and stays plain — it is not ours to interpret — and `event.note`
  stays plain because the log is not rendered anywhere.
- **Linkification is an AST transformer, not a regex.** goldmark parses, and a
  transformer visits text nodes only, which is the difference between linking
  an identifier and rewriting one inside a code span, inside the text of a link
  somebody already wrote, or inside an href. Where two patterns overlap —
  `ACME-1/tools#4` is both a Jira key and a pull request — the longer match
  wins. The plain path and the Markdown path share one definition of what an
  identifier is, so a reference cannot mean different things in a title and in
  a why.
- **goldmark is the parser, and the only non-obvious dependency here.** The
  standard library has no Markdown, and hand-rolling one is a tarpit: nested
  emphasis, unbalanced backticks and link-in-link are where those break, and
  every such bug would then be ours. goldmark is pure Go with no dependencies
  of its own, and it exposes exactly the hook this needed — an AST transformer
  — rather than only a string-to-HTML function.
- **Raw HTML is refused on input, not stripped at render.** It is the only
  thing worth validating in a Markdown field, since almost any string is valid
  Markdown, and it is worth it because the author finds out immediately. The
  parser decides what counts, rather than a second guess at Markdown's rules:
  `<https://example.com>` is an autolink and passes.
- **Settings are a table with columns, not a key-value bag.** The Jira base
  URL, the project keys worth linking and the name on the page live in
  `config`, one row enforced by a CHECK. Columns buy the diff-based event log
  — a settings change is exactly the sort of thing worth finding later — plus
  CHECK constraints and typing, where a string→string table gives none of
  that and invites `enable_foo = "true"`. The cost is a migration per setting,
  which for a handful is the right trade. `sequence` was already this shape.
- **The row is seeded by the migration, not by init**, so no reader has to
  decide what an absent configuration means, and an existing database comes up
  behaving exactly as it did. Validation lives in `Tx.SaveConfig` rather than
  in the command, for the same reason the field kinds do.
- **`--db` is the only setting that stays a flag**, because it says which
  database to open and cannot be read out of one that has not been chosen yet.
  `--jira-base-url` and `--jira-prefix` are gone; `TODO_JIRA_BASE_URL` and
  `TODO_JIRA_PREFIXES` remain as single-run overrides.
- **A long-running command exits when the database migrates under it.**
  `serve`, `syncer`, `watch` and `mcp` read the schema once, at startup, and
  every query afterwards assumes it; an upgrade applied by another process
  would otherwise surface as whatever query happened to run first. A guard
  service compares the applied-migration record against what was there at
  startup, every five seconds, and returns an error when it moves.
- **Exiting rather than reloading.** A restart is cheap and a process serving a
  schema it does not understand is not. `mcp` is respawned by its client, and
  `serve` already stops its other services when one fails, so the guard needs
  no mechanism of its own beyond being a service.
- **The check counts applied migrations as well as taking the highest.** The
  top version alone can stand still while the set grows, which is the
  out-of-order case `applied_migration` exists for in the first place. A read
  that fails is not a migration — a busy database is only a busy database, and
  the next tick asks again.
- **A pull request may name its own pipeline, and NULL means the
  repository's.** `pr.pipeline` is the only authored column a pull request
  has — everything else about one is observed, and the decision to track it is
  the row's existence. It is a judgement in the same way `github_repo.pipeline`
  is, so sync may not write it either.
- **The fallback is read at close, not copied at track.** Deliberately unlike
  `github_repo.pipeline` against the *default*, which resolves eagerly: a
  default is a guess made in the absence of policy and freezing it protects
  repositories already tracked, while a repository's pipeline **is** the policy
  and should reach the pull requests that never claimed an exception to it.
- **`todo pr set` exists so the exception is revocable.** A `--pipeline` only
  settable at track time would be a decision nobody could take back, and
  whether a change is a hotfix is usually learned afterwards. Changing it
  affects the next chain instantiated; actions already created are left alone,
  because they exist and something may be waiting on them.
- **The action ranking is the sketch's, with priority added as its first
  term.** `rank_pin`, then the priority of the project it advances, then
  `rank_class`, then the unblocks count descending, then effort ascending,
  then creation order. The sketch proposed the middle three and predates
  priority being used as the planning signal it now is; a one-click action on
  a barely-wanted project outranking real work on the most wanted one reads as
  the queue ignoring what it was told.
- **The unblocks count is transitive, and counts only open followers.** The
  sketch's argument for the term is "the ones gating chains", and a direct
  count gives the head of a chain of four the same 1 as something blocking a
  single leaf — it cannot see a chain at all. Freeing something already closed
  is worth nothing. `hidden_behind` is excluded: it is the judgement that a
  follower is not worth looking at, not a statement about ordering, and the
  sketch is explicit that conflating the two regrows the noise the fold rule
  removed.
- **Effort is the project's, because an action has no size of its own.** The
  sketch's `action` table has no effort column and its `project` table does,
  so "then effort ascending" can only mean the project's. An action's own size
  is already carried by `rank_class`.
- **Unstated sorts last at every term**, so filling nothing in never moves an
  item up, and nothing is inferred from a title or an age — a queue that
  quietly promotes things is one you stop trusting.
- **Relations are declared on the entity, naming a loader rather than a join.**
  What an action is connected to beyond its own columns is a list on the
  struct; the SQL stays hand-written and tested where it was. Generating the
  queries was never the value — one place to read, and one answer for every
  consumer, was.
- **`show` and `show -o json` now agree**, which closes the older complaint
  that the table printed an action's blockers and the JSON did not. A relation
  with nothing at the far end is left out rather than rendered empty.
- **The status page keeps its own batched loaders.** One query per relation is
  right for one record and wrong for a list, and the page reads these for
  every row it draws. Sharing would need a batch loader in each declaration,
  which is worth it when a third consumer appears and not before.
- **The subject pull request and the context ones are two relations, not one
  list with a role.** The schema allows at most one subject and closing reads
  only that; a context link is background and is never asked anything. Two
  relations put that rule in the shape instead of in a comment.
- **Staleness is a second clock, and `updated_at` is not it.** When something
  last changed is already recoverable from `updated_at` and the log; when
  someone last looked at it and was satisfied was recorded nowhere.
  `last_verified_at` is that, on projects and on actions — the sketch left it
  off actions, which is the wrong way round, since an action claims something
  is worth doing *now* and rots faster than a plan does.
- **`todo verify` writes an observed column, under an actor of its own.**
  Verifying is asking the world whether the record is still true, not deciding
  what it should say, which is why the sketch lists it beside `todo sync` as a
  writer of observed fields. Like `pr announce` it is a named command rather
  than an `--actor` override — one verb instead of a hole in the rule — and it
  logs as `sync:verify` so the log says a person went and looked. This settles
  the old open question, which had assumed the answer might be to make the
  column authored instead.
- **Never verified sorts first under `--sort staleness`**, inverting the rule
  that unstated sorts last. Same reasoning, different destination: a missing
  priority is an absence of information, a missing verification is the
  information.
- **No `verified` event kind**, though the sketch's vocabulary lists one. The
  diff already logs `last_verified_at` moving, and hand-writing a second event
  beside it is exactly what "events come from diffs, never hand-written calls"
  rules out. Worth revisiting only if something needs to filter on it.
- **`pr.checks` is a row per check, not a JSON blob.** One column holding the
  whole map could only ever diff as "the map changed", so every job starting
  or finishing re-emitted all of it — about fifteen useless events an hour
  across two pull requests. A row per check names the one that moved.
  `checks_state`, the rollup, stays on `pr`: that is GitHub's summary and it
  is what every reader was already acting on.
- **Every check transition is written; only some are logged.** That is not a
  hole in "every mutation is an event" but the argument `auto` columns already
  make: `pr.last_synced_at` is written and unlogged because it moves on every
  poll and would bury what matters. A check going green does the same. So
  crossing *into* a broken state is news, crossing back *out* is news, and
  everything else is written silently — which is what finally distinguishes
  newly broken from still broken.
- **PENDING is not broken.** A check that has not finished is not a failure,
  and treating it as one would raise something on every push. The broken set
  is `FAILURE`, `ERROR`, `CANCELLED`, `TIMED_OUT`.
- **A check GitHub stops reporting is deleted, not kept.** Contexts come and
  go with the workflow file, and a check nobody runs any more is not a check
  that failed. Its disappearance is logged only if it was broken, since that
  is a question being answered rather than a row being tidied.
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
- **`--unblocked` and `--waiting` partition what is in play**, and are built
  from the same conditions so they cannot disagree about it. The page asks the
  store for both rather than filtering one out of the other.
- **`--stale` is where a judgement and a fact disagree**: an action closed as
  completed whose subject pull request is still open. Only completion claims
  anything — an abandoned action with an open pull request is someone deciding
  not to finish, which is not a contradiction. A pull request nobody has
  synced is not evidence either way.
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
