# TODO

What is left, taken from the design sketch plus what has come up since. Items
are ordered roughly by what blocks what, not by importance.

## Where things stand

Built: the schema and its migration machinery; the diff-and-emit layer; six
entities — `project`, `action`, `pr`, `github_repo`, `calendar_window` and
`actionverb`; GitHub sync, one-shot and as a polling loop; the predicate
registry the verb vocabulary resolves against; and the pipelines a repository
chooses between. `roz watch` tails the log.

Every command in the tree is implemented; nothing is a stub any more.

The queue is mechanical, which was the claim the whole design rested on.
Closing a `write` action instantiates the repository's pipeline as a chain of
blocked actions; sync then closes each step as GitHub satisfies its predicate,
and each closure frees the next. Nobody types "the pull request merged".

## The critical path

Empty. Everything the sketch put in dependency order is built: the entities,
the pipelines, the cascade, sync closing what GitHub finishes, the queue
queries and a page to read them on. What is left is depth and polish, tracked in
issues, and none of it blocks anything else.

## What is left

**In [issues](https://github.com/scottlaird/roz/issues).** Everything that was
listed here as an open item now lives there, where it can be discussed, closed
by a pull request, and read by someone who is not holding this file in their
head. What follows is the record of what was decided, which is the part worth
keeping in the repository.

One thing did not become an issue, because it is not work:

- **`roz pr announce` is a habit, not a gap.** `send_for_review` closes on the
  announcement and GitHub cannot supply it, so somebody has to say so. The
  tooling half of it — noticing a `wait_review` whose pull request was never
  announced — is [#83](https://github.com/scottlaird/roz/issues/83).

Still genuinely open, and deliberately not an issue: **sync cadence versus
event fidelity.** The sketch's own question, mostly answered — the log records
transitions rather than poll results, so a quiet poll writes nothing. What
remains is whether a minute is often enough to catch states that do not
persist, `UNSTABLE` and `BEHIND` in particular. Worth an issue if it ever
bites; not worth one before.

## Settled, recorded so it is not relitigated

- **An issue belongs to a tracker, and the pair is its identity.** `jira_issue`
  became `tracker_issue` with a `tracker` discriminator, and the id is composed
  as `tracker:key` the way a pull request's is `repo#number`. Composed rather
  than trusting the keys not to collide: `CDSS-1744` and `owner/repo#123` do
  not today, but that is a property of two third parties rather than something
  the schema can enforce. The many-to-many was already right — 0008 settled
  that when one piece of work turned out to need two issues — so this only
  added the discriminator. `sprint` became `iteration`, being Jira's sprint and
  GitHub's milestone under one name. `github` is in the CHECK before anything
  can read it, so the schema is not what blocks that work.
- **`--tracker` defaults to `jira`, and that is a statement about today.**
  Every issue recorded so far is a Jira one and jira is the only tracker
  anything can read, so the default is right far more often than a required
  flag would be useful — and it is what lets `link-jira` survive as a plain
  alias of `link-issue`. It is still the tool assuming a fact, which is the
  argument that went the other way for `pr --because`. The difference is that
  the honest answer here is knowable and singular, where a tracking reason is
  neither. When a second tracker works this wants to become a `roz config`
  setting rather than a constant.

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
  `--jira-base-url` and `--jira-prefix` are gone; `ROZ_JIRA_BASE_URL` and
  `ROZ_JIRA_PREFIXES` remain as single-run overrides. The value is bound to
  the command tree rather than to a package variable, so two trees in one
  process each get their own — which `roz mcp` needs, since it builds a fresh
  tree per tool call.
- **A backup is `VACUUM INTO`, and it never overwrites.** A file copy can
  catch a torn state in WAL mode, where the committed data is split between
  the database and the `-wal`. SQLite refuses to write over an existing file
  and nothing softens that, so the default destination is timestamped —
  `backups/roz-<UTC>.db` beside the database — because any fixed name would
  work once. Restore is the asymmetric half: it validates the source before
  touching anything, refuses an existing database without `--replace`, and
  renames rather than deletes when it does replace one, since the database
  being replaced may be the reason for the restore.
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
- **`roz pr set` exists so the exception is revocable.** A `--pipeline` only
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
- **`roz verify` writes an observed column, under an actor of its own.**
  Verifying is asking the world whether the record is still true, not deciding
  what it should say, which is why the sketch lists it beside `roz sync` as a
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
- **`pr announce` settles, the same way sync does.** `send_for_review` closes
  on the announcement, so waiting for the next poll left the queue showing
  work that was already done for up to a sync interval. One settle pass, under
  the predicate actor, after the transaction rather than inside it — closing
  is its own unit of work, and a cascade that fails should not undo the fact
  that was reported.
- **It reports what it closed**, and that is not decoration. A command reading
  as "write down what I did in Slack" now closes actions and instantiates
  whatever follows them. That is the design working — nobody should have to
  type the fact and then separately type the consequence — but it means the
  command is not as innocuous as its name, and the output has to say so.
  `reportSettled` is shared with sync rather than duplicated, so the two
  cannot describe the same event differently.
- **A wait that has gone on too long raises an exception.** Excluding
  `rank_class = wait` from `--unblocked` was right — four of ten queue items
  were waits — but it left nothing speaking up when one went bad. `severity =
  'exception'` is what a monitor already filters on, so this reuses it rather
  than inventing a second alerting path.
- **Reported, never changed.** An overdue action is not closed, snoozed or
  reprioritised. What to do about a stuck wait is a judgement, and this is
  only the part that says a judgement is wanted. **It is not a snooze**: a
  snooze hides something until a date, this reveals something after one.
- **The allowance is `actionverb.wait_days`, and NULL means never.** A verb
  describing your own work cannot be overdue, only undone; only `wait_review`
  (3 days) and `merge` (1) are seeded. `action.okay_to_wait_until` is the
  per-action exception, resolved when the deadline is checked rather than
  copied at creation — the same shape `pr.pipeline` has against its
  repository. The repository was considered as a second source and left out:
  one place to look beats two until something needs the second.
- **The clock is `waiting_since`, which finally means something.** The sketch
  called it "derivable from review requests" and nothing had derived it, so
  the column was dead and every wait would have been counted from whenever the
  action happened to be created. Sync fills it from the subject pull request's
  `first_review_requested_at`, falling back to `created_at` where nothing has
  observed a wait beginning.
- **Idempotent through the log, with no new state.** A wait is already
  reported if an exception exists at or after its deadline. If the deadline
  later moves out — a fresh review request, or a raised allowance — the old
  exception falls before the new one and it is reported again, which is right:
  it is a different wait.
- **Checked even when nothing is tracked.** A deadline is not a fact about
  GitHub, and `Sync`'s nothing-to-poll return used to skip it entirely.
- **One project can block another, and the status follows the edge.**
  `project.status` accepted `blocked` from the start with nothing recording
  what it was blocked on, so it was a status with no referent: the dependency
  lived in a summary, and nothing ever cleared it. `project_blocks` is
  deliberately the same shape as `action_blocks`, down to the CHECK — it is
  the same relationship between different rows, and two spellings of it would
  be one too many.
- **Closing a project frees what was waiting on it.** That cascade existed for
  actions and not for projects, so a blocked project stayed blocked for ever
  unless somebody remembered. There is no project equivalent of
  `hidden_behind`: folding something out of a queue is a judgement about a
  queue, and the project table is not one.
- **A blocked project is shown, not hidden.** The page filtered to `active`,
  so blocking one made it vanish — and blocked is exactly where work goes
  quiet, which makes it the last thing to hide. `ProjectFilter.Open` is what
  the page reads now.
- **Blocked state is decided from a fresh read, never the caller's copy.**
  Deciding from a stale status writes the old world back: a project snoozed
  since it was loaded would be quietly woken. The schema catches that one,
  because `snooze_until` and `status` are coupled by a CHECK, which is how it
  was found — and `applyBlockedState` had the same bug for actions, where
  nothing would have caught it at all.
- **Why a pull request is tracked is a column; that it is tracked is still the
  row's existence.** Two different questions, and the second only arises once
  you track something you did not write — a review has different actions and a
  different reason to stop tracking it. `authored`, `reviewing`, `watching`,
  as a CHECK rather than free text, because things branch on it and an
  unrecognised value would be a silent gap rather than a new case.
- **No default.** Assuming `authored` would be right most of the time and
  would still be the tool inventing a fact it cannot check — and for every row
  predating the column, one it has no way to check at all. NULL means
  unstated, which is the honest answer to a question nobody was asked.
- **It has a reader from the day it lands**, which is the `waiting_since`
  lesson: `pr list --because reviewing` answers "what am I on the hook to
  review". A column nothing reads is a column nothing writes either.
- **It does not finish the `review` verb.** Closing that on a predicate also
  needs our GitHub login, and `config.owner` is deliberately a label rather
  than one — so this is necessary and not sufficient.
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
  integration has `CDSS-1744`, not `ROZ106`. Every project carrying the key
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
- **One renderer, used twice.** `roz serve` calls the same function `roz
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
- **A predicate reads a `Facts` struct, not a `*PR`.** `ref_exists` asks about
  a repository and an expression and has no subject pull request at all, so the
  signature had to widen. The six that only want the pull request go through an
  `onPR` adapter, which is also where "no subject means false" lives — one
  place rather than the top of each.
- **A ref wait is a semver constraint, not a name or a glob.** `>=1.5` says
  "the next release" without knowing whether it will be v1.5.0 or v1.5.1, which
  a literal cannot and a glob can only approximate. Constraints also settle
  pre-releases by a published rule rather than a local invention: `>=1.2` does
  not match v1.3.0-rc1, and `>=1.2.0-0` does. `github.com/Masterminds/semver/v3`
  parses both halves — a leaf package, pure Go, no cgo, nothing transitive.
  Lenient enough that `v1.5.0`, `1.5.0` and `1.5` all read as versions.
- **An expression that is not a constraint is a literal name or a glob.** The
  only way to wait on a ref no version scheme describes — a `release-1.5`
  branch being cut — and the two forms cannot be confused, since `release-1.5`
  does not parse as a constraint. One flag rather than two, because a wait is
  one thing and the reading is unambiguous.
- **A series is identified by its path prefix, compared for equality.** A
  monorepo tags `v1.2.3` and, disjointly, `api/v3.4.5`; the numbers of one mean
  nothing to the other, so `api/>=3.6` and `>=1.2` can never satisfy each
  other. An absent prefix means the top level, *not* "any prefix" — otherwise
  `>=1.2` would be answered by `api/v2.3.4`, which is both numerically true and
  entirely wrong. The final slash divides path from constraint, which is
  unambiguous because no constraint contains one; a colon was the alternative,
  and lost only because `api/>=3.6` looks like the tag it selects.
- **Which refs to poll is derived from the outstanding waits**, not configured
  per repository. The poll set is then right by construction: nothing is asked
  about that nothing waits for, and a wait cannot name a repository somebody
  forgot to add to a list. Two costs, both real. Refs are only observed while
  something waits for one, so roz cannot answer "when was v1.4.0 cut" for a
  release nobody gated on. And a top-level wait has no path to narrow on, so a
  monorepo returns its most recent tags across every component — which is why
  polling targets are the one thing that may yet belong on the repository,
  tracked as [#128](https://github.com/scottlaird/roz/issues/128).
- **Only a repository's first read walks its history; later polls stop at what
  they recognise.** Tags come back newest-first, so a page carrying a ref
  already recorded means the read has met what the last one left and everything
  below is older still. A repository with eight hundred tags and nothing new
  costs one request rather than five, and the boundary is the set of names
  already stored — read once per poll rather than asked per ref.
- **Reaching the bound is a statement about history, not a complaint about the
  expression.** An earlier version said "narrow the path prefix" and raised an
  action; that is wrong whenever the prefix is already exact, which it usually
  is — a repository simply having a long history is not a filter problem, and a
  queue item advising a fix that does not apply is worse than no item. What it
  now says is what it means: refs created from now on will be seen, and a wait
  for one that already exists and is older than the first read may not be.
- **Refs are read newest first, paged to a bound, and reaching that bound is
  reported.** Tags come back newest-commit-first, so a
  forward-looking wait is answered by the first page; paging exists for the
  repositories where one page is not the whole history. The bound exists
  because some are unreasonable — `aws/aws-sdk-go-v2` has 82,000 tags and
  GitHub answers page four of that connection about two times in three.
- **Branches are read alphabetically, so a branch wait rests on its filter.**
  GitHub orders refs by name or by tag commit date and nothing else, and a
  branch has no commit date — so alphabetical it is, which is unrelated to what
  anybody waits for. `facebook/react` has 945 branches whose release ones sort
  past any bounded read. What makes it work is that the poll filter carries the
  literal head of a name-shaped expression as well as the path prefix, turning
  945 into three. A constraint contributes no filter, since `>=1.2` is not a
  substring of any name. Recorded as thin in
  [#129](https://github.com/scottlaird/roz/issues/129).
- **A matcher matches either as a constraint or as a name, whichever hits.**
  `releases/19.2.x` is a real branch and also parses as the constraint 19.2.*,
  and the branch's own name is not a version — so a constraint-only reading
  would never match the ref it was written to name. The union adds no false
  matches: a constraint-shaped matcher is not a name any ref has, and a
  name-shaped one does not parse, so the arms overlap only where a name is also
  a version.
- **An exception for a standing condition is logged once a day, not once a
  poll.** Sync re-derives the world every few seconds, so a pull request that
  has gone invisible or a repository too large for the ref feed is found again
  on every pass. Logging each one buries the log a monitor watches and teaches
  whoever reads it to skip that source, which costs more than the repeats do —
  the first notice was worth having. Keyed on the condition, never the message:
  two problems on one repository stay separate, and a detail moving does not
  defeat it. The log is the record, the same way the overdue query already
  decides whether a wait has been reported. `roz exception` is untouched, since
  a person recording one deliberately is not a poll restating itself.
- **A ref page that fails stops that batch rather than the sync.** A connection
  large enough to need paging is large enough for GitHub to time out serving
  it, and one unreadable repository must not take the pull request poll down
  with it. Rate limiting still propagates, because that means wait rather than
  something being wrong with the question.
- **A repository's first poll is a backfill, not news.** It sees the whole tag
  history at once — a hundred releases that existed long before anybody waited
  for one — so those are counted rather than listed. Only what appears after
  that is worth a line.

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
`roz pr announce` and `roz project jira` cover both by hand, which is enough
to work with and enough to know what the real sync has to produce.
Without it, `send_for_review` cannot close on its own — that verb is the only
predicate GitHub cannot satisfy.
