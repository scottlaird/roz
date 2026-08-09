// Package ghsync applies GitHub's view of a pull request to the store.
//
// It is the only writer of observed pull request columns, and it writes them
// as sync:github, so the actor rule in the store rejects any attempt to touch
// an authored one. It never writes to GitHub.
package ghsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/scottlaird/todo/internal/github"
	"github.com/scottlaird/todo/internal/store"
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

	// RateLimit is what GitHub last said about the budget, so a caller
	// polling on a loop can pace itself.
	RateLimit github.RateLimit
}

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
		return result, nil
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
		changes, err := applyOne(ctx, st, observed)
		if err != nil {
			return Result{}, err
		}
		if len(changes) > 0 {
			result.Changed[observed.Key] = changes
		}
	}

	for key, why := range fetched.Missing {
		result.Missing[key] = why
		if err := reportMissing(ctx, st, key, why); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

func applyOne(ctx context.Context, st *store.Store, observed github.PullRequest) ([]store.Change, error) {
	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, observed.Key)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", observed.Key, err)
	}

	after, err := merge(before, observed)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", observed.Key, err)
	}

	changes, err := tx.Update(ctx, before, after)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changes, nil
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

	checks, err := encodeChecks(observed.Checks)
	if err != nil {
		return nil, err
	}
	after.Checks = checks

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

// encodeChecks renders the rollup as a JSON object with sorted keys, so an
// unchanged set of checks encodes identically each time and does not read as
// a change.
func encodeChecks(checks map[string]string) (string, error) {
	if len(checks) == 0 {
		return "{}", nil
	}
	// encoding/json sorts map keys, so this is already stable.
	encoded, err := json.Marshal(checks)
	if err != nil {
		return "", fmt.Errorf("encoding checks: %w", err)
	}
	return string(encoded), nil
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
