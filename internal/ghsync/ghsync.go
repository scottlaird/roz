// Package ghsync applies GitHub's view of a pull request to the store, and
// closes the actions that view has finished.
//
// It is the only writer of observed pull request columns, and it writes them
// as sync:github, so the actor rule in the store rejects any attempt to touch
// an authored one. It never writes to GitHub.
//
// Settling is a separate act under a separate actor. Recording that a pull
// request is merged is an observation; deciding that the merge action is
// therefore done is a rule a person wrote into the vocabulary, so it is
// written as `predicate` rather than as sync:github.
package ghsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// Fetcher is the part of github.Client this package needs, so a test can
// stand in for it.
type Fetcher interface {
	Fetch(ctx context.Context, keys []string) (github.Result, error)
	Refs(ctx context.Context, queries []github.RefQuery) (github.RefResult, error)
}

// Result reports what one sync did.
type Result struct {
	// Polled is how many pull requests were asked about.
	Polled int
	// Changed lists the keys whose stored state moved, with the columns that
	// moved for each.
	Changed map[string][]store.Change
	// Missing lists keys GitHub would not resolve, and why. An exception
	// event is logged for each, since a tracked pull request going invisible
	// is something a person should hear about.
	Missing map[string]string

	// Overdue lists the actions that have been waiting longer than their verb
	// allows. Reported, not changed: what to do about one is a judgement.
	Overdue []store.Overdue
	// Unannounced lists the waits on a review nobody was asked for. Reported
	// the same way and for the same reason, and a different problem: a slow
	// review is chased, and one nobody requested is announced.
	Unannounced []store.Unannounced
	// Woken are the actions whose deferral ran out this cycle and are back in
	// play. The other end of such a wait -- the issue closing -- arrives
	// through Settle and is reported as a close, which is what makes the two
	// distinguishable.
	Woken []*store.Action
	// Settled lists the actions closed because what was observed satisfied
	// their predicate, with whatever each closure cascaded into.
	Settled []store.Settled
	// Ejected lists the pull requests that left the merge queue without
	// merging. An exception is logged for each, and an action created unless
	// something open already covered merging it.
	Ejected []store.Ejected

	// IssuesPolled is how many tracked issues were asked about. Counted apart
	// from Polled because "polled 0" over a database of nothing but issues
	// would otherwise read as though the sync had done nothing.
	IssuesPolled int
	// Issues lists the tracked issues whose state moved this poll.
	Issues []store.TrackerApplied
	// IssuesClosedWithWork lists the issues that closed while actions against
	// them were still open, as the note each was reported with.
	IssuesClosedWithWork []string
	// Owners lists the pull requests whose required reviewers changed, worked
	// out from the files they touch against the repository's CODEOWNERS.
	Owners []Owners
	// Resolved lists the release gates given a concrete version this poll.
	// A gate instantiates without one, since working it out needs the
	// repository's tags read first.
	Resolved []store.Resolved
	// NewRefs lists the branches and tags that appeared since the last poll.
	// A release being cut is news whether or not it satisfied anything.
	//
	// Refs recorded by a repository's *first* poll are not here: see
	// Backfilled. Everything roz knows about a repository arrives at once
	// that time, and none of it appeared in any sense a person means.
	NewRefs []*store.GitRef
	// Backfilled counts what a first poll recorded, keyed "repo kind".
	Backfilled map[string]int
	// Truncated lists the ref filters too broad to read to the end. An
	// exception is logged for each, and an action raised: a filter that cannot
	// be read through is a wait that may never close, which is worse than one
	// that closes late.
	Truncated []github.RefTruncation
	// RefsPolled is how many repository-and-kind pairs were asked about,
	// which is zero whenever nothing is waiting for a ref.
	RefsPolled int

	// RateLimit is what GitHub last said about the budget, so a caller
	// polling on a loop can pace itself.
	RateLimit github.RateLimit

	// asked counts the requests this cycle actually made, as opposed to the
	// reads it considered making. A read with nothing to poll makes no
	// request and is neither a success nor a failure — counting it as one
	// would decide "did anything work" on a read that never happened.
	asked int

	// Stacked lists the pull requests whose stacked_on moved this cycle,
	// either onto a parent or off one.
	Stacked []*store.PR

	// Teams counts the team memberships refreshed this cycle, which is zero
	// unless a step is waiting for a group and its answer had gone stale.
	Teams int

	// Failed lists the reads that did not happen this cycle. Empty is the
	// ordinary case.
	//
	// A cycle makes several reads and one failing no longer cancels the rest,
	// so "the sync worked" is no longer a yes or no. What did succeed is
	// applied and reported; this is how the rest is not lost with it.
	Failed []ReadFailure
}

// ReadFailure is one read a cycle could not make.
type ReadFailure struct {
	// Read names it — refs, issues, pull requests — so a log line says what
	// went unread rather than only that something did.
	Read string
	Err  error
}

func (f ReadFailure) Error() string { return f.Read + ": " + f.Err.Error() }

// readError marks a failure as GitHub's rather than the store's.
//
// The distinction decides whether a cycle carries on: a read that GitHub
// refused leaves the others worth attempting, and a database that will not
// answer does not.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

// OverdueCount is how many waits have gone on too long.
func (r Result) OverdueCount() int { return len(r.Overdue) }

// SettledCount is how many actions closed on their own.
func (r Result) SettledCount() int { return len(r.Settled) }

// EjectedCount is how many pull requests fell out of a merge queue.
func (r Result) EjectedCount() int { return len(r.Ejected) }

// ChangedCount is how many pull requests moved.
func (r Result) ChangedCount() int { return len(r.Changed) }

// Sync polls every tracked pull request and records what changed.
//
// Each pull request is applied in its own transaction. One correlation id per
// pull request rather than one for the whole run is deliberate: a sync is not
// a single command, and grouping unrelated changes under one id would make
// "why did this change?" unanswerable in exactly the way correlation exists
// to prevent.
func Sync(ctx context.Context, st *store.Store, client Fetcher) (Result, error) {
	result := Result{Changed: map[string][]store.Change{}, Missing: map[string]string{}}

	// Refs first, and unconditionally: an action can wait for a release in a
	// database with no pull requests tracked at all, and the observation has
	// to be in place before Settle asks whether it arrived. That ordering is
	// why the fix for a failing read is to stop cancelling the rest rather
	// than to put the cheap ones first.
	if err := syncRefs(ctx, st, client, &result); err != nil {
		if stop, err := noteFailure(&result, readRefs, err); stop {
			return result, err
		}
	}

	// Issues too, and for the same reason: a project can track one in a
	// database with no pull requests at all.
	if reader, ok := client.(IssueReader); ok {
		if err := syncIssues(ctx, st, reader, &result); err != nil {
			if stop, err := noteFailure(&result, readIssues, err); stop {
				return result, err
			}
		}
	}

	if err := syncPRs(ctx, st, client, &result); err != nil {
		if stop, err := noteFailure(&result, readPRs, err); stop {
			return result, err
		}
	}

	// Membership, for the groups an open step is waiting for a review from.
	// Bounded by what is waiting rather than by the organisation, and only
	// when the stored answer has aged out, so an ordinary cycle makes no
	// request at all.
	if reader, ok := client.(TeamReader); ok {
		if err := syncTeams(ctx, st, reader, time.Now(), &result); err != nil {
			if stop, err := noteFailure(&result, readTeams, err); stop {
				return result, err
			}
		}
	}

	// After the state poll, so a head that moved this minute is derived
	// against rather than against the one before it.
	//
	// Not in the partial-failure scheme, because it already is one: an
	// unreadable pull request there is reported and the rest carry on, so
	// anything reaching here is the store rather than GitHub.
	if reader, ok := client.(ChangeReader); ok {
		if err := syncOwners(ctx, st, reader, &result); err != nil {
			return result, err
		}
	}

	// After the pull requests are applied, so a base branch that changed this
	// minute is resolved against what GitHub just said rather than against
	// the previous poll. Local, so a cycle that could not read anything still
	// re-derives from what is stored.
	stacked, err := st.ResolveStacking(ctx, store.ActorSyncGitHub)
	if err != nil {
		return result, err
	}
	result.Stacked = stacked

	// Settling on a partly-read cycle is safe, and it is worth saying why: an
	// observation that did not happen records nothing, so a predicate simply
	// does not fire. Absence is not a negative observation. A fix that
	// "completed" a partial cycle by clearing what it had not read would break
	// exactly that, and would close or reopen things on no evidence.
	//
	// Settle after applying everything, not per pull request: a chain can span
	// several, and a step freed by one closure may be satisfied by an
	// observation made in the same pass.
	settled, err := st.Settle(ctx, store.ActorPredicate)
	if err != nil {
		return result, err
	}
	result.Settled = settled

	// After settling, so a step that just closed is not also reported as
	// having waited too long. Settle is about what finished; this is about
	// what has not, and finishing wins.
	//
	// Unconditional, because a deadline is not a fact about GitHub: an action
	// can sit past its allowance in a database with nothing tracked at all,
	// and on a cycle where every read failed.
	if err := checkOverdue(ctx, st, &result); err != nil {
		return result, err
	}

	// A cycle that got nothing at all from GitHub is a failed cycle rather
	// than a partial one, and is what the caller counts towards giving up.
	// Anything less is reported through Result.Failed and left to the caller,
	// which is what stops one broken read from cancelling the others.
	if result.asked == 0 && len(result.Failed) > 0 {
		return result, fmt.Errorf("every read failed: %w", result.Failed[0].Err)
	}
	return result, nil
}

// The reads one cycle makes, named so a failure says which one it was.
const (
	readRefs   = "refs"
	readIssues = "issues"
	readPRs    = "pull requests"
	readTeams  = "team membership"
)

// noteFailure records a read that did not happen, and says whether the cycle
// can carry on without it.
//
// A failure that did not come from GitHub stops the cycle. The store is what
// every read writes into, so a database that will not answer is not a partial
// success — it is the same failure arriving four times.
//
// A rate limit stops it too, for a different reason: the reads share one
// GraphQL budget, so carrying on after one has been refused spends against a
// limit already hit. Attempting all of them is the rule for faults; a limit is
// not a fault.
func noteFailure(result *Result, read string, err error) (bool, error) {
	var fault *readError
	if !errors.As(err, &fault) {
		return true, err
	}
	result.Failed = append(result.Failed, ReadFailure{Read: read, Err: fault.err})
	if errors.Is(err, github.ErrRateLimited) {
		return true, err
	}
	return false, nil
}

// syncPRs polls the pull requests that can still move, and applies what
// GitHub said about them.
func syncPRs(ctx context.Context, st *store.Store, client Fetcher, result *Result) error {
	// Not every tracked pull request: the ones that can still move, plus a
	// window past an ending for the comments that land after one, plus
	// anything an open action is about. See store.PRsToPoll.
	settings, err := st.Config(ctx)
	if err != nil {
		return err
	}
	keys, err := st.PRsToPoll(ctx, store.PollWindow{Days: settings.PollWindowDays})
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	result.Polled = len(keys)

	fetched, err := client.Fetch(ctx, keys)
	if err != nil {
		return &readError{err: err}
	}
	result.asked++
	result.RateLimit = fetched.RateLimit

	for _, observed := range fetched.PullRequests {
		changes, ejected, err := applyOne(ctx, st, observed)
		if err != nil {
			return err
		}
		if len(changes) > 0 {
			result.Changed[observed.Key] = changes
		}
		if ejected {
			// Reported outside the transaction that observed it, because
			// creating an action allocates an identifier, and that is its own
			// unit of work.
			report, err := st.ReportEjection(ctx, observed.Key)
			if err != nil {
				return err
			}
			result.Ejected = append(result.Ejected, *report)
		}
	}

	// Only for the keys this read actually asked about. A read that never
	// ran reports nothing missing: not polled is not missing, and reporting
	// it that way would raise an action per entity and turn one broken cycle
	// into a queue full of spurious work.
	for key, why := range fetched.Missing {
		result.Missing[key] = why
		if err := reportMissing(ctx, st, key, why); err != nil {
			return err
		}
	}
	return nil
}

// syncRefs polls the refs something is waiting for, and records them.
//
// The poll set comes from the outstanding waits rather than from
// per-repository configuration. That makes it exactly right by construction:
// nothing is asked about that nothing is waiting for, so a busy repository
// costs nothing until it is relevant, and a wait cannot be written against a
// repository somebody forgot to add to a watch list.
//
// The cost is that refs are only observed while something waits for one. roz
// therefore cannot answer "when was v1.4.0 cut" for a release nobody gated on,
// which is a question it was never asked.
func syncRefs(ctx context.Context, st *store.Store, client Fetcher, result *Result) error {
	waits, err := st.OpenRefWaits(ctx)
	if err != nil {
		return err
	}
	// A gate with no version yet is also a reason to read a repository — the
	// only reason, in fact, since nothing is waiting on it until it resolves.
	// Without this the tags it counts from would never be fetched.
	pending, err := st.PendingRefs(ctx)
	if err != nil {
		return err
	}
	for _, p := range pending {
		waits = append(waits, store.RefWait{
			RepoID: p.RepoID, Kind: p.Kind,
			PathPrefix: p.Series(),
			Matcher:    ">=0.0.0",
		})
	}
	queries, err := refQueries(ctx, st, waits)
	if err != nil {
		return err
	}
	if len(queries) == 0 {
		return nil
	}
	result.RefsPolled = len(queries)

	fetched, err := client.Refs(ctx, queries)
	if err != nil {
		return &readError{err: err}
	}
	result.asked++
	if fetched.RateLimit.Known() {
		result.RateLimit = fetched.RateLimit
	}

	// Asked before anything is written, since writing is what makes it false.
	backfilling, err := firstPollOf(ctx, st, queries)
	if err != nil {
		return err
	}

	for _, observed := range fetched.Refs {
		ref, isNew, err := applyRef(ctx, st, observed)
		if err != nil {
			return err
		}
		if !isNew {
			continue
		}
		if key := ref.RepoID + " " + ref.Kind; backfilling[key] {
			if result.Backfilled == nil {
				result.Backfilled = map[string]int{}
			}
			result.Backfilled[key]++
			continue
		}
		result.NewRefs = append(result.NewRefs, ref)
	}

	// A repository that will not resolve is reported the way an unreadable
	// pull request is: an exception against the repository, which is a fact
	// worth keeping whether or not anybody reads it today.
	for repo, why := range fetched.Missing {
		if err := reportUnreadableRepo(ctx, st, repo, why); err != nil {
			return err
		}
	}

	// Reported to the caller only when it was reported to the log, so a
	// syncer polling every few seconds does not restate it either.
	// Resolved after the refs are recorded, so a gate written a moment ago is
	// answered by this poll rather than the next one.
	for _, p := range pending {
		resolved, err := st.ResolvePendingRef(ctx, p)
		if err != nil {
			return err
		}
		if resolved != nil {
			result.Resolved = append(result.Resolved, *resolved)
		}
	}

	for _, t := range fetched.Truncated {
		reported, err := reportTruncated(ctx, st, t)
		if err != nil {
			return err
		}
		if reported {
			result.Truncated = append(result.Truncated, t)
		}
	}
	return nil
}

// eventRefsTruncated is raised when a ref filter matches more than one read
// can get through.
const eventRefsTruncated = "ref_poll_truncated"

// reportTruncated says that a repository's history was not read to the end.
//
// What that does and does not mean is the whole content of the message. Refs
// created from now on arrive at the top of the feed and are seen: a wait for
// something that has not happened yet is unaffected, which is nearly every
// wait. What is not covered is a wait for a ref that *already exists* and is
// old enough to fall outside the first read.
//
// No action is raised. An earlier version told the reader to narrow the path
// prefix, which is wrong whenever the prefix is already exact — a repository
// simply having eight hundred tags is not a filter problem, and an item in the
// queue advising a fix that does not apply is worse than no item.
func reportTruncated(ctx context.Context, st *store.Store, t github.RefTruncation) (bool, error) {
	why := fmt.Sprintf(
		"%s: %d refs match and the newest %d were read, so history was not reached to the end. "+
			"Refs created from now on will be seen; a wait for one that already exists and is older than that may not be.",
		t.Query.Repo, t.Matched, t.Read)

	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	subject, err := tx.LoadGitHubRepo(ctx, t.Query.Repo)
	if err != nil {
		return false, nil
	}
	// Once a day rather than once a poll. The filter being too broad is a
	// standing fact about the repository, not something that happens.
	reported, err := tx.ExceptionOnce(ctx, subject, eventRefsTruncated, why)
	if err != nil {
		return false, err
	}
	return reported, tx.Commit()
}

// firstPollOf reports which of the repositories about to be read have never
// been read before, keyed the way the caller counts them.
func firstPollOf(ctx context.Context, st *store.Store, queries []github.RefQuery) (map[string]bool, error) {
	first := map[string]bool{}
	for _, q := range queries {
		kind := store.RefBranch
		if q.Prefix == store.RefPath(store.RefTag, "") {
			kind = store.RefTag
		}
		key := q.Repo + " " + kind
		if _, asked := first[key]; asked {
			continue
		}
		known, err := st.HasRefs(ctx, q.Repo, kind)
		if err != nil {
			return nil, err
		}
		first[key] = !known
	}
	return first, nil
}

// refQueries turns the outstanding waits into the smallest set of questions
// that answers all of them.
//
// Waits collapse by repository, namespace and literal prefix: three actions
// waiting for v1.5.0, v1.6.0 and v2.0.0 all ask GitHub the same thing, and
// asking once is the difference between a query per item and a query per
// repository.
func refQueries(ctx context.Context, st *store.Store, waits []store.RefWait) ([]github.RefQuery, error) {
	type key struct{ repo, prefix, path, contains string }

	seen := map[key]bool{}
	var queries []github.RefQuery
	for _, w := range waits {
		k := key{w.RepoID, store.RefPath(w.Kind, ""), w.PathPrefix, w.PollFilter()}
		if seen[k] {
			continue
		}
		seen[k] = true

		// What this repository and kind already hold, so the read can stop as
		// soon as it meets it. One query per poll rather than one per ref: a
		// set of a few hundred names is cheap to hold and is what turns a
		// five-page re-read into a single request.
		known, err := st.RefNames(ctx, w.RepoID, w.Kind)
		if err != nil {
			return nil, err
		}
		queries = append(queries, github.RefQuery{
			Repo:     k.repo,
			Prefix:   k.prefix,
			Path:     k.path,
			Contains: k.contains,
			Known: func(name string) bool {
				return known[name]
			},
		})
	}
	return queries, nil
}

// applyRef records one observed ref, reporting whether it had not been seen
// before.
func applyRef(ctx context.Context, st *store.Store, observed github.Ref) (*store.GitRef, bool, error) {
	kind := store.RefBranch
	if observed.Prefix == store.RefPath(store.RefTag, "") {
		kind = store.RefTag
	}

	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	ref := store.NewGitRef(observed.Repo, kind, observed.Name, observed.CommitSHA)
	isNew, err := tx.ObserveRef(ctx, ref)
	if err != nil {
		return nil, false, fmt.Errorf("recording %s: %w", ref.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return ref, isNew, nil
}

// eventUnreadableRepo is raised when a repository something waits on will not
// resolve.
const eventUnreadableRepo = "repo_unresolvable"

// reportUnreadableRepo logs an exception against a repository GitHub would not
// resolve. It changes nothing: a repository renamed or made private is a
// judgement, not something to act on automatically.
func reportUnreadableRepo(ctx context.Context, st *store.Store, repo, why string) error {
	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	subject, err := tx.LoadGitHubRepo(ctx, repo)
	if err != nil {
		// Nothing useful to attach it to. A wait can name a repository that
		// was never tracked, and that is the command's business, not sync's.
		return nil
	}
	// A repository that will not resolve stays unresolvable, and sync sees it
	// afresh every poll. One notice a day, not one a poll.
	if _, err := tx.ExceptionOnce(ctx, subject, eventUnreadableRepo, why); err != nil {
		return err
	}
	return tx.Commit()
}

// checkOverdue records the waits that have gone on too long.
//
// Its own function because it runs on both paths out of Sync: whether or not
// there was anything to poll, an action can be past its deadline.
func checkOverdue(ctx context.Context, st *store.Store, result *Result) error {
	overdue, err := st.OverdueWaits(ctx, store.ActorPredicate)
	if err != nil {
		return err
	}
	result.Overdue = overdue

	// Structural rather than timed, so it is checked here beside the deadline
	// sweep rather than waiting for one: a wait on a review nobody requested
	// is wrong the moment it starts, and the timeout would eventually report
	// it with the wrong explanation. See store.UnannouncedWaits.
	unannounced, err := st.UnannouncedWaits(ctx, store.ActorPredicate)
	if err != nil {
		return err
	}
	result.Unannounced = unannounced

	// Deferrals whose date has arrived. Here rather than in a sweep of its own
	// for the reason the comment above gives: this runs on both paths out of
	// Sync, and a date passes whether or not there was anything to poll.
	//
	// A wake rather than an exception. A snoozed action is invisible to the
	// overdue check on purpose -- a deferred action is not late -- so the date
	// arriving has to bring it back into play instead of reporting it. See
	// store.WakeExpired.
	woken, err := st.WakeExpired(ctx, store.ActorPredicate)
	if err != nil {
		return err
	}
	result.Woken = woken
	return nil
}

// applyOne writes one observation, and reports whether it was the moment the
// pull request fell out of a merge queue.
func applyOne(ctx context.Context, st *store.Store, observed github.PullRequest) ([]store.Change, bool, error) {
	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, observed.Key)
	if err != nil {
		return nil, false, fmt.Errorf("loading %s: %w", observed.Key, err)
	}

	after, err := merge(before, observed)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", observed.Key, err)
	}

	changes, err := tx.Update(ctx, before, after)
	if err != nil {
		return nil, false, err
	}

	// Read from the two records rather than from the diff: what matters is
	// which way the value moved and what the state is now, and a list of
	// changed columns says neither.
	ejected := store.EjectedFromMergeQueue(before, after)

	// The checks are rows rather than a column, so they are written beside
	// the update rather than diffed with it — in the same transaction, so a
	// failure leaves neither half applied. ApplyChecks decides for itself
	// which transitions are worth logging; most are not.
	if err := tx.ApplyChecks(ctx, observed.Key, observed.Checks); err != nil {
		return nil, false, err
	}

	// The issues this pull request closes, as rows rather than a column, for
	// the same reason the checks are. Reconciled rather than accumulated: a
	// closing keyword taken out of a body is GitHub saying the pull request
	// no longer closes that issue.
	//
	// An empty list is a fact here, unlike an empty column in merge() — it is
	// GitHub reporting that the body names no issues, which is exactly the
	// case reconciling has to act on. Only sync's own rows are touched; a
	// person's link to the same issue survives.
	if err := tx.ReconcileClosingIssues(ctx, observed.Key, observed.ClosingIssues); err != nil {
		return nil, false, err
	}

	// When reviewers could first have seen it is a fact about the pull
	// request, and the actions waiting on it inherit it. This is the only
	// thing that ever writes action.waiting_since, which the sketch describes
	// as "derivable from review requests" and nothing had derived.
	if err := tx.ObserveWaitingSince(ctx, observed.Key, observed.FirstReviewRequestedAt); err != nil {
		return nil, false, err
	}

	// Commit even when no column moved: a check may have, and that is a
	// change to the pull request whether or not the rollup noticed.
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return changes, ejected, nil
}

// merge produces the record GitHub says should exist.
//
// Absence is not a fact: where GitHub reported nothing, the stored value is
// left as it is rather than being cleared. Overwriting a known value with an
// empty one would log a change that did not happen — most visibly with
// mergeStateStatus, which GitHub reports as UNKNOWN for a merged pull request
// and while it is still computing.
func merge(before *store.PR, observed github.PullRequest) (*store.PR, error) {
	after := before.Clone()

	if observed.Title != "" {
		after.Title = observed.Title
	}
	after.State = keepIfEmpty(before.State, observed.State)
	after.IsDraft = boolValue(observed.IsDraft)
	after.InMergeQueue = boolValue(observed.InMergeQueue)
	after.Author = keepIfEmpty(before.Author, observed.Author)
	after.URL = keepIfEmpty(before.URL, observed.URL)
	after.BaseRef = keepIfEmpty(before.BaseRef, observed.BaseRef)
	after.HeadSHA = keepIfEmpty(before.HeadSHA, observed.HeadSHA)
	after.HeadRef = keepIfEmpty(before.HeadRef, observed.HeadRef)
	after.ReviewDecision = keepIfEmpty(before.ReviewDecision, observed.ReviewDecision)
	after.MergeStateStatus = keepIfEmpty(before.MergeStateStatus, observed.MergeStateStatus)
	after.ChecksState = keepIfEmpty(before.ChecksState, observed.ChecksState)
	after.FirstReviewRequestedAt = keepIfEmpty(before.FirstReviewRequestedAt, observed.FirstReviewRequestedAt)
	after.HumanCommentedAt = keepIfEmpty(before.HumanCommentedAt, observed.HumanCommentedAt)
	// A merge is permanent, so the empty value GitHub sends for anything not
	// merged never has to unset this. keepIfEmpty is exactly that rule, and
	// the reason it is right here rather than merely convenient.
	after.MergedAt = keepIfEmpty(before.MergedAt, observed.MergedAt)
	// An ending is permanent in the same way a merge is, so the empty value
	// GitHub sends for an open pull request never has to unset this. A
	// reopened one keeps the date it closed on, and state is what says it is
	// open again.
	after.ClosedAt = keepIfEmpty(before.ClosedAt, observed.ClosedAt)
	// Zero is a real answer here — no unresolved threads — so unlike the
	// text columns this is always written.
	after.UnresolvedThreads = sql.NullInt64{Int64: int64(observed.UnresolvedThreads), Valid: true}

	teams, err := encodeStrings(observed.ReviewerTeams)
	if err != nil {
		return nil, err
	}
	after.ReviewerTeams = teams

	approvals, err := encodeStrings(observed.Approvals)
	if err != nil {
		return nil, err
	}
	after.Approvals = approvals

	return after, nil
}

// encodeStrings renders a list as a JSON array, sorted for the same reason.
func encodeStrings(values []string) (string, error) {
	if len(values) == 0 {
		return "[]", nil
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)

	encoded, err := json.Marshal(sorted)
	if err != nil {
		return "", fmt.Errorf("encoding a list: %w", err)
	}
	return string(encoded), nil
}

// reportMissing logs an exception against a pull request GitHub would not
// resolve. It changes nothing: whether to stop tracking it is a judgement.
func reportMissing(ctx context.Context, st *store.Store, key, why string) error {
	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	subject, err := tx.LoadSubject(ctx, st, key)
	if err != nil {
		// It is tracked, so it should load; if it does not, there is nothing
		// useful to attach the exception to.
		return nil
	}
	// Same standing condition as the two above: sync re-derives it on every
	// poll, so the log gets one notice a day rather than one each time.
	if _, err := tx.ExceptionOnce(ctx, subject, eventUnresolvable, why); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// A tracked pull request that has gone invisible needs a judgement —
	// whether to stop tracking it — and nothing else will make that decision.
	// Raised after the exception commits, for the reason the ejection is: the
	// observation is the half that must not be lost.
	_, err = st.RaiseAction(ctx, eventUnresolvable, subject,
		fmt.Sprintf("decide what to do about %s", key),
		fmt.Sprintf("%s could not be read: %s. Deleted, made private, or no longer visible.", key, why))
	return err
}

// eventUnresolvable is raised when a tracked pull request goes invisible.
const eventUnresolvable = "pr_unresolvable"

// keepIfEmpty returns the observed value, or the stored one when GitHub said
// nothing. Absence of information is not information.
func keepIfEmpty(before sql.NullString, observed string) sql.NullString {
	if observed == "" {
		return before
	}
	return sql.NullString{String: observed, Valid: true}
}

func boolValue(b bool) sql.NullBool {
	return sql.NullBool{Bool: b, Valid: true}
}
