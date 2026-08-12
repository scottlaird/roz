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
	"fmt"
	"sort"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// Fetcher is the part of github.Client this package needs, so a test can
// stand in for it.
type Fetcher interface {
	Fetch(ctx context.Context, keys []string) (github.Result, error)
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
	// Settled lists the actions closed because what was observed satisfied
	// their predicate, with whatever each closure cascaded into.
	Settled []store.Settled
	// Ejected lists the pull requests that left the merge queue without
	// merging. An exception is logged for each, and an action created unless
	// something open already covered merging it.
	Ejected []store.Ejected

	// RateLimit is what GitHub last said about the budget, so a caller
	// polling on a loop can pace itself.
	RateLimit github.RateLimit
}

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

	tracked, err := st.ListPRs(ctx, store.PRFilter{})
	if err != nil {
		return Result{}, err
	}
	if len(tracked) == 0 {
		// Nothing to poll, but a deadline is not a fact about GitHub: an
		// action can sit past its allowance in a database with no pull
		// requests tracked at all.
		return result, checkOverdue(ctx, st, &result)
	}

	keys := make([]string, len(tracked))
	for i, pr := range tracked {
		keys[i] = pr.ID
	}
	result.Polled = len(keys)

	fetched, err := client.Fetch(ctx, keys)
	if err != nil {
		return Result{}, err
	}
	result.RateLimit = fetched.RateLimit

	for _, observed := range fetched.PullRequests {
		changes, ejected, err := applyOne(ctx, st, observed)
		if err != nil {
			return Result{}, err
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
				return Result{}, err
			}
			result.Ejected = append(result.Ejected, *report)
		}
	}

	for key, why := range fetched.Missing {
		result.Missing[key] = why
		if err := reportMissing(ctx, st, key, why); err != nil {
			return Result{}, err
		}
	}

	// Settle after applying everything, not per pull request: a chain can
	// span several, and a step freed by one closure may be satisfied by an
	// observation made in the same pass.
	result.Settled, err = st.Settle(ctx, store.ActorPredicate)
	if err != nil {
		return Result{}, err
	}

	// After settling, so a step that just closed is not also reported as
	// having waited too long. Settle is about what finished; this is about
	// what has not, and finishing wins.
	if err := checkOverdue(ctx, st, &result); err != nil {
		return Result{}, err
	}
	return result, nil
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
	after.ReviewDecision = keepIfEmpty(before.ReviewDecision, observed.ReviewDecision)
	after.MergeStateStatus = keepIfEmpty(before.MergeStateStatus, observed.MergeStateStatus)
	after.ChecksState = keepIfEmpty(before.ChecksState, observed.ChecksState)
	after.FirstReviewRequestedAt = keepIfEmpty(before.FirstReviewRequestedAt, observed.FirstReviewRequestedAt)
	after.HumanCommentedAt = keepIfEmpty(before.HumanCommentedAt, observed.HumanCommentedAt)
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
	if err := tx.Exception(ctx, subject, "pr_unresolvable", why); err != nil {
		return err
	}
	return tx.Commit()
}

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
