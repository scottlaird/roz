package ghsync

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// fakeFetcher returns a fixed answer and records what it was asked for.
type fakeFetcher struct {
	result github.Result
	err    error
	asked  []string

	refs      github.RefResult
	refErr    error
	askedRefs []github.RefQuery
}

func (f *fakeFetcher) Fetch(_ context.Context, keys []string) (github.Result, error) {
	f.asked = append(f.asked, keys...)
	if f.err != nil {
		return github.Result{}, f.err
	}
	return f.result, nil
}

func (f *fakeFetcher) Refs(_ context.Context, queries []github.RefQuery) (github.RefResult, error) {
	f.askedRefs = append(f.askedRefs, queries...)
	if f.refErr != nil {
		return github.RefResult{}, f.refErr
	}
	return f.refs, nil
}

// newStore returns a Store over a fresh database with one tracked repository
// and one tracked pull request in it.
func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "roz.db")
	if _, err := store.Init(path, map[store.Entity]string{
		store.EntityProject: "SL", store.EntityAction: "NA",
	}); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := store.New(db)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Insert(ctx, store.NewGitHubRepo("owner", "repo")); err != nil {
		t.Fatalf("tracking the repository: %v", err)
	}
	if err := tx.Insert(ctx, store.NewPR("owner/repo", 1)); err != nil {
		t.Fatalf("tracking the pull request: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return st, "owner/repo#1"
}

func loadPR(t *testing.T, st *store.Store, key string) *store.PR {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	pr, err := tx.LoadPR(ctx, key)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	return pr
}

func observed(key string) github.PullRequest {
	return github.PullRequest{
		Key: key, Repo: "owner/repo", Number: 1,
		Title: "a title", Author: "someone", URL: "https://example/1",
		State: "OPEN", IsDraft: true, BaseRef: "main", HeadSHA: "abc123",
		ReviewDecision: "REVIEW_REQUIRED", MergeStateStatus: "BLOCKED",
		ChecksState:   "SUCCESS",
		Checks:        map[string]string{"build": "SUCCESS"},
		ReviewerTeams: []string{"platform"},
	}
}

func TestSyncWritesObservedState(t *testing.T) {
	st, key := newStore(t)
	client := &fakeFetcher{result: github.Result{
		PullRequests: []github.PullRequest{observed(key)},
	}}

	result, err := Sync(context.Background(), st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if result.Polled != 1 || result.ChangedCount() != 1 {
		t.Errorf("result = polled %d, changed %d, want 1 and 1", result.Polled, result.ChangedCount())
	}

	pr := loadPR(t, st, key)
	if pr.State.String != "OPEN" || !pr.IsDraft.Bool {
		t.Errorf("state = %+v / draft %+v", pr.State, pr.IsDraft)
	}
	if pr.ReviewDecision.String != "REVIEW_REQUIRED" || pr.MergeStateStatus.String != "BLOCKED" {
		t.Errorf("review = %v, merge = %v", pr.ReviewDecision, pr.MergeStateStatus)
	}
	if got := checksFor(t, st, key); got != "build SUCCESS" {
		t.Errorf("checks = %q, want %q", got, "build SUCCESS")
	}
	if pr.ReviewerTeams != `["platform"]` {
		t.Errorf("reviewer_teams = %q", pr.ReviewerTeams)
	}
}

// TestSecondSyncIsQuiet is the property that keeps the log readable: polling
// again with the same answer must write nothing at all.
func TestSecondSyncIsQuiet(t *testing.T) {
	st, key := newStore(t)
	client := &fakeFetcher{result: github.Result{
		PullRequests: []github.PullRequest{observed(key)},
	}}
	ctx := context.Background()

	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("first Sync() returned error: %v", err)
	}
	before := loadPR(t, st, key)

	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("second Sync() returned error: %v", err)
	}
	if result.ChangedCount() != 0 {
		t.Errorf("second sync reported %d changed, want 0: %v", result.ChangedCount(), result.Changed)
	}

	after := loadPR(t, st, key)
	if after.LastSyncedAt != before.LastSyncedAt {
		t.Errorf("last_synced_at moved on a no-op sync: %v → %v", before.LastSyncedAt, after.LastSyncedAt)
	}
}

// TestLastSyncedAtTracksChanges pins the choice made about that column: it
// moves when the stored state moves, and is not logged.
func TestLastSyncedAtTracksChanges(t *testing.T) {
	st, key := newStore(t)
	ctx := context.Background()

	if got := loadPR(t, st, key).LastSyncedAt; got.Valid {
		t.Errorf("last_synced_at = %v on a freshly tracked pull request, want NULL", got)
	}

	client := &fakeFetcher{result: github.Result{
		PullRequests: []github.PullRequest{observed(key)},
	}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	pr := loadPR(t, st, key)
	if !pr.LastSyncedAt.Valid {
		t.Fatal("last_synced_at is still NULL after a sync that changed things")
	}

	// It is auto, so it is not in the log.
	for _, change := range changesFor(t, st, key) {
		if change == "last_synced_at" {
			t.Error("last_synced_at was logged, want it kept out of the log")
		}
	}
}

// changesFor lists the columns the log records as having changed.
func changesFor(t *testing.T, st *store.Store, key string) []string {
	t.Helper()
	events, err := st.Events(context.Background(), store.EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	var columns []string
	for _, e := range events {
		if e.SubjectID == key && e.Field != "" {
			columns = append(columns, e.Field)
		}
	}
	return columns
}

// TestAbsenceIsNotAFact covers the merge rule: GitHub saying nothing must not
// erase something already known.
func TestAbsenceIsNotAFact(t *testing.T) {
	st, key := newStore(t)
	ctx := context.Background()

	full := observed(key)
	if _, err := Sync(ctx, st, &fakeFetcher{result: github.Result{
		PullRequests: []github.PullRequest{full},
	}}); err != nil {
		t.Fatalf("first Sync() returned error: %v", err)
	}

	// A later poll where GitHub reports no merge state — a merged pull
	// request, or one still being computed.
	quiet := full
	quiet.MergeStateStatus = ""
	quiet.ReviewDecision = ""
	if _, err := Sync(ctx, st, &fakeFetcher{result: github.Result{
		PullRequests: []github.PullRequest{quiet},
	}}); err != nil {
		t.Fatalf("second Sync() returned error: %v", err)
	}

	pr := loadPR(t, st, key)
	if pr.MergeStateStatus.String != "BLOCKED" {
		t.Errorf("merge_state_status = %v, want the earlier value kept", pr.MergeStateStatus)
	}
	if pr.ReviewDecision.String != "REVIEW_REQUIRED" {
		t.Errorf("review_decision = %v, want the earlier value kept", pr.ReviewDecision)
	}
}

// TestSyncCannotWriteAuthoredColumns is the actor rule reaching sync. The
// only authored thing about a pull request is that it is tracked, so there is
// nothing for sync to break — but the guard should still be in force.
func TestSyncRunsAsSyncGitHub(t *testing.T) {
	st, key := newStore(t)
	client := &fakeFetcher{result: github.Result{
		PullRequests: []github.PullRequest{observed(key)},
	}}
	if _, err := Sync(context.Background(), st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	events, err := st.Events(context.Background(), store.EventQuery{Kind: "changed"})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no change events were logged")
	}
	for _, e := range events {
		if e.Actor != string(store.ActorSyncGitHub) {
			t.Errorf("event actor = %q, want %q", e.Actor, store.ActorSyncGitHub)
		}
	}
}

// TestMissingRaisesAnException covers a tracked pull request GitHub will no
// longer show us: it is not an error, but a person should hear about it.
func TestMissingRaisesAnException(t *testing.T) {
	st, key := newStore(t)
	client := &fakeFetcher{result: github.Result{
		Missing: map[string]string{key: "is not a pull request GitHub will show us"},
	}}

	result, err := Sync(context.Background(), st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.Missing) != 1 {
		t.Fatalf("missing = %v, want one entry", result.Missing)
	}

	events, err := st.Events(context.Background(), store.EventQuery{Severity: store.SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) != 1 || events[0].SubjectID != key {
		t.Errorf("exception events = %v, want one against %s", events, key)
	}
}

func TestSyncWithNothingTracked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roz.db")
	if _, err := store.Init(path, map[store.Entity]string{
		store.EntityProject: "SL", store.EntityAction: "NA",
	}); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()
	st, err := store.New(db)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	client := &fakeFetcher{}
	result, err := Sync(context.Background(), st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if result.Polled != 0 || len(client.asked) != 0 {
		t.Errorf("polled %d and asked for %v, want neither", result.Polled, client.asked)
	}
}

// checksFor reads the check rows back as one comparable string.
func checksFor(t *testing.T, st *store.Store, key string) string {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	checks, err := tx.ChecksFor(ctx, key)
	if err != nil {
		t.Fatalf("ChecksFor() returned error: %v", err)
	}
	return store.FormatChecks(checks)
}

// TestRepeatedChecksAreQuiet keeps the property the old encodeChecks test
// guarded, now that there is no encoding to be stable about: polling again
// with the same answers must not read as a change.
//
// Map iteration order used to be the hazard, because the whole map was one
// value. Rows are keyed by name and written only when the state differs, so
// ordering cannot leak — but the property is worth pinning wherever it lives.
func TestRepeatedChecksAreQuiet(t *testing.T) {
	st, key := newStore(t)
	ctx := context.Background()

	pr := observed(key)
	pr.Checks = map[string]string{"a": "FAILURE", "b": "SUCCESS", "c": "PENDING"}
	client := &fakeFetcher{result: github.Result{PullRequests: []github.PullRequest{pr}}}

	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("first Sync() returned error: %v", err)
	}
	before, err := st.Events(ctx, store.EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}

	for range 5 {
		if _, err := Sync(ctx, st, client); err != nil {
			t.Fatalf("Sync() returned error: %v", err)
		}
	}
	after, err := st.Events(ctx, store.EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("five quiet polls wrote %d events, want none", len(after)-len(before))
	}
}

// linkedAction creates an action with a predicate verb whose subject is the
// pull request, which is what a pipeline step is.
func linkedAction(t *testing.T, st *store.Store, verb, key string) *store.Action {
	t.Helper()
	ctx := context.Background()

	a := store.NewAction(verb+" it", verb)
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, a); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, key, store.RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return a
}

// TestSyncSettlesWhatItObserves is the join between the two halves: sync
// records that the pull request merged, and the merge action goes away
// without anyone closing it.
func TestSyncSettlesWhatItObserves(t *testing.T) {
	st, key := newStore(t)
	merge := linkedAction(t, st, "merge", key)
	undraft := linkedAction(t, st, "undraft", key)

	pr := observed(key)
	pr.State = "MERGED"
	pr.IsDraft = true // still a draft, so undraft is not satisfied
	client := &fakeFetcher{result: github.Result{PullRequests: []github.PullRequest{pr}}}

	result, err := Sync(context.Background(), st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	if result.SettledCount() != 1 {
		t.Fatalf("settled %d actions, want 1", result.SettledCount())
	}
	if got := result.Settled[0].Action.ID; got != merge.ID {
		t.Errorf("settled %s, want the merge action %s", got, merge.ID)
	}
	if result.Settled[0].PR != key {
		t.Errorf("settled against %q, want %q", result.Settled[0].PR, key)
	}

	// The one GitHub did not finish is untouched.
	if loadAction(t, st, undraft.ID).State == "done" {
		t.Error("the undraft action closed on a pull request that is still a draft")
	}
}

func loadAction(t *testing.T, st *store.Store, id string) *store.Action {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	a, err := tx.LoadAction(ctx, id)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	return a
}

// TestSyncSettlesNothingWhenNothingIsObserved: a poll that learns nothing
// must not close anything, or an unreachable GitHub would empty the queue.
func TestSyncSettlesNothingWhenNothingIsObserved(t *testing.T) {
	st, key := newStore(t)
	linkedAction(t, st, "merge", key)

	client := &fakeFetcher{result: github.Result{
		Missing: map[string]string{key: "is not a pull request GitHub will show us"},
	}}

	result, err := Sync(context.Background(), st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if result.SettledCount() != 0 {
		t.Errorf("settled %d actions on an unreadable pull request, want none",
			result.SettledCount())
	}
}

// TestSyncObservesWaitingSince: the sketch calls waiting_since "derivable
// from review requests" and nothing had derived it, so the column meant
// nothing and every wait clock started from whenever the action happened to
// be created.
func TestSyncObservesWaitingSince(t *testing.T) {
	st, key := newStore(t)
	ctx := context.Background()

	action := linkedAction(t, st, "wait_review", key).ID

	pr := observed(key)
	pr.FirstReviewRequestedAt = "2026-08-01T09:00:00.000Z"
	client := &fakeFetcher{result: github.Result{PullRequests: []github.PullRequest{pr}}}

	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	a, err := tx.LoadAction(ctx, action)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if a.WaitingSince.String != "2026-08-01T09:00:00.000Z" {
		t.Errorf("waiting_since = %v, want the review request time", a.WaitingSince)
	}
}

// TestSyncLeavesWaitingSinceAloneWhenGitHubSaysNothing: absence is not a
// fact, the same rule the column merge follows.
func TestSyncLeavesWaitingSinceAloneWhenGitHubSaysNothing(t *testing.T) {
	st, key := newStore(t)
	ctx := context.Background()

	action := linkedAction(t, st, "wait_review", key).ID
	client := &fakeFetcher{result: github.Result{
		PullRequests: []github.PullRequest{observed(key)},
	}}

	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	a, err := tx.LoadAction(ctx, action)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if a.WaitingSince.Valid {
		t.Errorf("waiting_since = %v, want it left unset", a.WaitingSince)
	}
}

// bareStore is a store with nothing tracked in it — no repository, no pull
// request — which is the other path out of Sync.
func bareStore(t *testing.T) *store.Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "roz.db")
	if _, err := store.Init(path, map[store.Entity]string{
		store.EntityProject: "SL", store.EntityAction: "NA",
	}); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := store.New(db)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	return st
}

// TestOverdueIsCheckedWithNothingTracked: a deadline is not a fact about
// GitHub. An action can sit past its allowance in a database with no pull
// requests tracked at all, and the nothing-to-poll return used to skip the
// check entirely.
func TestOverdueIsCheckedWithNothingTracked(t *testing.T) {
	st := bareStore(t)
	ctx := context.Background()

	a := store.NewAction("wait for somebody", "wait_review")
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}
	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Insert(ctx, a); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	// Past its deadline by the authored route rather than by backdating a
	// clock. An action created now cannot have been waiting since 2020 — that
	// is the state #131 was about — and this test is only about whether Sync
	// runs the check at all when it has no pull requests to poll.
	overdue, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	long := a.Clone()
	long.OkayToWaitUntil = sql.NullString{String: "2020-01-01T00:00:00.000Z", Valid: true}
	if _, err := overdue.Update(ctx, a, long); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := overdue.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	result, err := Sync(ctx, st, &fakeFetcher{})
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if result.OverdueCount() != 1 {
		t.Errorf("overdue = %d with nothing tracked, want 1", result.OverdueCount())
	}
}

// queued is an observation of a pull request sitting in the merge queue.
func queued(key string) github.PullRequest {
	pr := observed(key)
	pr.IsDraft = false
	pr.InMergeQueue = true
	pr.State = "OPEN"
	pr.MergeStateStatus = "CLEAN"
	pr.ReviewDecision = "APPROVED"
	return pr
}

// syncWith runs one pass against a fixed answer.
func syncWith(t *testing.T, st *store.Store, prs ...github.PullRequest) Result {
	t.Helper()
	client := &fakeFetcher{result: github.Result{PullRequests: prs}}
	result, err := Sync(context.Background(), st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	return result
}

// TestEjectionIsReported: a pull request thrown out of the merge queue looks
// exactly like one that was never in it, and nothing is waiting on anyone.
func TestEjectionIsReported(t *testing.T) {
	st, key := newStore(t)
	syncWith(t, st, queued(key))

	left := queued(key)
	left.InMergeQueue = false // still OPEN

	result := syncWith(t, st, left)
	if result.EjectedCount() != 1 {
		t.Fatalf("ejected = %d, want 1", result.EjectedCount())
	}
	if result.Ejected[0].Action == nil {
		t.Fatal("nothing was put in the queue for the ejection")
	}

	events, err := st.Events(context.Background(), store.EventQuery{
		Severity: store.SeverityException,
	})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) != 1 || events[0].Kind != store.EventLeftMergeQueue {
		t.Errorf("exceptions = %v, want one %s", events, store.EventLeftMergeQueue)
	}
}

// TestAMergeIsNotAnEjection is the caveat that matters most. The common
// true → false transition is a pull request landing, and keying on the
// transition alone would raise an exception on every one that does.
func TestAMergeIsNotAnEjection(t *testing.T) {
	st, key := newStore(t)
	syncWith(t, st, queued(key))

	merged := queued(key)
	merged.InMergeQueue = false
	merged.State = "MERGED"

	result := syncWith(t, st, merged)
	if result.EjectedCount() != 0 {
		t.Errorf("a successful merge was reported as an ejection: %v", result.Ejected)
	}

	events, err := st.Events(context.Background(), store.EventQuery{
		Severity: store.SeverityException,
	})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("a merge raised %d exceptions, want none", len(events))
	}
}

// TestEjectionAddsOneActionForRepeatedShuffles: a queue that reshuffles can
// eject and re-add within a minute, and an item per shuffle is noise.
func TestEjectionAddsOneActionForRepeatedShuffles(t *testing.T) {
	st, key := newStore(t)
	left := queued(key)
	left.InMergeQueue = false

	syncWith(t, st, queued(key))
	first := syncWith(t, st, left)
	syncWith(t, st, queued(key)) // re-queued
	second := syncWith(t, st, left)

	if first.Ejected[0].Action == nil {
		t.Fatal("the first ejection created no action")
	}
	if second.EjectedCount() != 1 {
		t.Fatalf("the second ejection was not reported: %v", second.Ejected)
	}
	// Reported again, because it happened again — but the queue already holds
	// the thing to do about it.
	if second.Ejected[0].Action != nil {
		t.Errorf("the second ejection added %s, want the open one to cover it",
			second.Ejected[0].Action.ID)
	}
}

// TestEjectionDefersToAPipelineStep: an ordinary tracked pull request already
// has a merge step, and the ejection is news rather than a new task.
func TestEjectionDefersToAPipelineStep(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	syncWith(t, st, queued(key))

	// A merge action already covering this pull request.
	a := store.NewAction("merge it", "merge")
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}
	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Insert(ctx, a); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, key, store.RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	left := queued(key)
	left.InMergeQueue = false
	result := syncWith(t, st, left)

	if result.EjectedCount() != 1 {
		t.Fatalf("the ejection was not reported: %v", result.Ejected)
	}
	if result.Ejected[0].Action != nil {
		t.Errorf("added %s alongside the existing merge step", result.Ejected[0].Action.ID)
	}
}

// TestNeverQueuedIsNotAnEjection: absence is not a fact, so a pull request
// whose in_merge_queue was never true has not left anything.
func TestNeverQueuedIsNotAnEjection(t *testing.T) {
	st, key := newStore(t)

	notQueued := observed(key)
	notQueued.InMergeQueue = false

	if result := syncWith(t, st, notQueued); result.EjectedCount() != 0 {
		t.Errorf("a pull request that was never queued was reported: %v", result.Ejected)
	}
	// And again, in case the first pass was doing the work.
	if result := syncWith(t, st, notQueued); result.EjectedCount() != 0 {
		t.Errorf("a second quiet poll reported an ejection: %v", result.Ejected)
	}
}

// TestAnUnreadablePullRequestReachesTheQueue: a tracked pull request that has
// gone invisible needs a judgement — whether to stop tracking it — and until
// now that judgement lived only in a log line nobody reads.
func TestAnUnreadablePullRequestReachesTheQueue(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	client := &fakeFetcher{result: github.Result{
		Missing: map[string]string{key: "is not a pull request GitHub will show us"},
	}}

	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	actions, err := st.ListActions(ctx, store.ActionFilter{Open: true})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("%d actions after an unreadable pull request, want 1", len(actions))
	}
	if !strings.Contains(actions[0].Title, key) {
		t.Errorf("title = %q, want it to name the pull request", actions[0].Title)
	}

	// This exception fires on every poll while the condition lasts, so the
	// queue must not grow an item each time.
	for range 3 {
		if _, err := Sync(ctx, st, client); err != nil {
			t.Fatalf("Sync() returned error: %v", err)
		}
	}
	actions, err = st.ListActions(ctx, store.ActionFilter{Open: true})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if len(actions) != 1 {
		t.Errorf("%d actions after four polls, want the same one", len(actions))
	}
}

// waitFor adds an open action waiting on a ref, which is the only thing that
// makes sync ask GitHub about a repository's refs at all.
func addRefWait(t *testing.T, st *store.Store, w store.RefWait) *store.Action {
	t.Helper()
	ctx := context.Background()

	a := store.NewAction("wait for a release", "wait_ref")
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, a); err != nil {
		t.Fatalf("inserting the action: %v", err)
	}
	w.ActionID = a.ID
	if err := tx.SetRefWait(ctx, w); err != nil {
		t.Fatalf("SetRefWait() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return a
}

// TestSyncAsksOnlyAboutRefsSomethingWaitsFor is the design in one test: the
// poll set is derived from outstanding waits, so a database with none costs
// no ref query at all.
func TestSyncAsksOnlyAboutRefsSomethingWaitsFor(t *testing.T) {
	st, key := newStore(t)
	client := &fakeFetcher{result: github.Result{PullRequests: []github.PullRequest{observed(key)}}}

	if _, err := Sync(context.Background(), st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(client.askedRefs) != 0 {
		t.Errorf("Sync() asked about %v refs with nothing waiting", client.askedRefs)
	}
}

// TestSyncCollapsesWaitsIntoOneQuery: three actions waiting for different
// releases of the same repository ask GitHub the same question, and asking it
// once is the difference between a query per item and a query per repository.
func TestSyncCollapsesWaitsIntoOneQuery(t *testing.T) {
	st, key := newStore(t)
	for _, matcher := range []string{">=1.5", ">=1.6", ">=2.0"} {
		addRefWait(t, st, store.RefWait{
			RepoID: "owner/repo", Kind: store.RefTag, Matcher: matcher,
		})
	}
	client := &fakeFetcher{result: github.Result{PullRequests: []github.PullRequest{observed(key)}}}

	result, err := Sync(context.Background(), st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(client.askedRefs) != 1 {
		t.Fatalf("Sync() asked %d ref queries for three waits on one series, want 1: %v",
			len(client.askedRefs), client.askedRefs)
	}
	got := client.askedRefs[0]
	if got.Repo != "owner/repo" || got.Prefix != "refs/tags/" || got.Contains != "" {
		t.Errorf("asked %s %s %q, want owner/repo refs/tags/ with no filter",
			got.Repo, got.Prefix, got.Contains)
	}
	if result.RefsPolled != 1 {
		t.Errorf("RefsPolled = %d, want 1", result.RefsPolled)
	}
}

// TestSyncClosesTheWaitWhenTheReleaseAppears is the whole feature through
// sync: the tag turns up and the action is gone, with no date involved.
func TestSyncClosesTheWaitWhenTheReleaseAppears(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	a := addRefWait(t, st, store.RefWait{
		RepoID: "owner/repo", Kind: store.RefTag, Matcher: ">=1.5",
	})

	client := &fakeFetcher{
		result: github.Result{PullRequests: []github.PullRequest{observed(key)}},
		refs: github.RefResult{Refs: []github.Ref{
			// The release that already shipped is still there, and must not
			// be mistaken for the one being waited for.
			{Repo: "owner/repo", Prefix: "refs/tags/", Name: "v1.4.0", CommitSHA: "old"},
		}},
	}

	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.Settled) != 0 {
		t.Fatalf("Sync() closed something on a release older than the bound")
	}
	// The first poll of a repository sees its whole history at once. None of
	// that appeared in any sense a person means, so it is counted, not listed.
	if len(result.NewRefs) != 0 {
		t.Errorf("NewRefs = %v on a first poll, want none", result.NewRefs)
	}
	if got := result.Backfilled["owner/repo tag"]; got != 1 {
		t.Errorf("Backfilled[owner/repo tag] = %d, want 1", got)
	}

	client.refs.Refs = append(client.refs.Refs,
		github.Ref{Repo: "owner/repo", Prefix: "refs/tags/", Name: "v1.5.0", CommitSHA: "new"})

	result, err = Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.Settled) != 1 || result.Settled[0].Action.ID != a.ID {
		t.Fatalf("Sync() settled %v, want [%s]", result.Settled, a.ID)
	}
	// Now the repository is known, a tag turning up is news — and only the
	// new one is: v1.4.0 was already there.
	if len(result.NewRefs) != 1 || result.NewRefs[0].Name != "v1.5.0" {
		t.Errorf("NewRefs = %v, want just v1.5.0", result.NewRefs)
	}
	if len(result.Backfilled) != 0 {
		t.Errorf("Backfilled = %v on a second poll, want none", result.Backfilled)
	}

	// And the query stops being asked, because nothing waits any more.
	client.askedRefs = nil
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(client.askedRefs) != 0 {
		t.Errorf("Sync() still asked %v after the wait closed", client.askedRefs)
	}
}

// TestSyncPollsRefsWithNoPullRequestsTracked: a release gate is not about a
// pull request, so it must work in a database that tracks none.
func TestSyncPollsRefsWithNoPullRequestsTracked(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)

	a := addRefWait(t, st, store.RefWait{
		RepoID: "owner/repo", Kind: store.RefBranch, Matcher: "release-1.5",
	})
	client := &fakeFetcher{refs: github.RefResult{Refs: []github.Ref{
		{Repo: "owner/repo", Prefix: "refs/heads/", Name: "release-1.5", CommitSHA: "sha"},
	}}}

	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.Settled) != 1 || result.Settled[0].Action.ID != a.ID {
		t.Fatalf("Sync() settled %v, want [%s]", result.Settled, a.ID)
	}
}

// newBareStore is newStore without the pull request: a database that tracks a
// repository and nothing in it, which is the state a release gate can be the
// only thing in.
func newBareStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "roz.db")
	if _, err := store.Init(path, map[store.Entity]string{
		store.EntityProject: "SL", store.EntityAction: "NA",
	}); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := store.New(db)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Insert(ctx, store.NewGitHubRepo("owner", "repo")); err != nil {
		t.Fatalf("tracking the repository: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return st
}

// TestSyncReportsAnUnreadablePullRequestOnce is the reported bug through sync:
// a condition that is true on every poll writes one line, not one per poll.
func TestSyncReportsAnUnreadablePullRequestOnce(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	client := &fakeFetcher{result: github.Result{
		Missing: map[string]string{key: "is not a pull request GitHub will show us"},
	}}

	for i := 0; i < 6; i++ {
		if _, err := Sync(ctx, st, client); err != nil {
			t.Fatalf("Sync() returned error: %v", err)
		}
	}

	events, err := st.Events(ctx, store.EventQuery{Severity: store.SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	var reported int
	for _, e := range events {
		if e.Kind == "pr_unresolvable" && e.SubjectID == key {
			reported++
		}
	}
	if reported != 1 {
		t.Errorf("six polls logged %d exceptions for one standing condition, want 1", reported)
	}
}

// TestSyncReportsATruncatedFilterOnce is the same for the ref feed, and also
// checks that the caller is told only when the log was.
func TestSyncReportsATruncatedFilterOnce(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	addRefWait(t, st, store.RefWait{
		RepoID: "owner/repo", Kind: store.RefTag, Matcher: ">=1.5",
	})

	client := &fakeFetcher{refs: github.RefResult{
		Truncated: []github.RefTruncation{{
			Query:   github.RefQuery{Repo: "owner/repo", Prefix: "refs/tags/"},
			Matched: 82234, Read: 500,
		}},
	}}

	first, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(first.Truncated) != 1 {
		t.Fatalf("the first sync reported %d truncations, want 1", len(first.Truncated))
	}

	for i := 0; i < 5; i++ {
		again, err := Sync(ctx, st, client)
		if err != nil {
			t.Fatalf("Sync() returned error: %v", err)
		}
		if len(again.Truncated) != 0 {
			t.Fatalf("poll %d restated the truncation", i+2)
		}
	}

	events, err := st.Events(ctx, store.EventQuery{Severity: store.SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	var reported int
	for _, e := range events {
		if e.Kind == "ref_poll_truncated" {
			reported++
		}
	}
	if reported != 1 {
		t.Errorf("six polls logged %d truncation exceptions, want 1", reported)
	}
}

// TestASecondPollCostsOneRequest is the reported case: a repository with more
// tags than one page, waited on for something that has not happened yet. The
// first poll walks what it can; every poll after it should ask once and stop
// as soon as it recognises something.
func TestASecondPollCostsOneRequest(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	addRefWait(t, st, store.RefWait{
		RepoID: "owner/repo", Kind: store.RefTag, Matcher: ">=9.0",
	})

	// Two pages of history, then nothing new.
	var page int
	client := &pagingFetcher{next: func(known func(string) bool) github.RefResult {
		page++
		var refs []github.Ref
		for i := 0; i < 3; i++ {
			refs = append(refs, github.Ref{
				Repo: "owner/repo", Prefix: "refs/tags/",
				Name: fmt.Sprintf("v1.%d.0", i), CommitSHA: "sha",
			})
		}
		return github.RefResult{Refs: refs, Missing: map[string]string{}}
	}}

	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("first sync made %d ref reads, want 1", client.calls)
	}
	// Everything the second poll sees is already recorded.
	known, err := st.RefNames(ctx, "owner/repo", store.RefTag)
	if err != nil {
		t.Fatalf("RefNames() returned error: %v", err)
	}
	if len(known) != 3 {
		t.Fatalf("recorded %d refs, want 3", len(known))
	}

	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.NewRefs) != 0 {
		t.Errorf("the second poll called %d refs new", len(result.NewRefs))
	}
	// And the query it asked carried the boundary, which is what lets the
	// client stop after one page.
	if client.lastKnown == nil {
		t.Fatal("the second poll asked without saying what it already had")
	}
	if !client.lastKnown("v1.0.0") {
		t.Error("the known set does not contain a ref that was recorded")
	}
	if client.lastKnown("v9.9.9") {
		t.Error("the known set claims a ref that was never recorded")
	}
}

// pagingFetcher records what the ref query was told, so a test can check that
// the boundary reaches the client.
type pagingFetcher struct {
	next      func(known func(string) bool) github.RefResult
	calls     int
	lastKnown func(string) bool
}

func (f *pagingFetcher) Fetch(context.Context, []string) (github.Result, error) {
	return github.Result{Missing: map[string]string{}}, nil
}

func (f *pagingFetcher) Refs(_ context.Context, queries []github.RefQuery) (github.RefResult, error) {
	f.calls++
	if len(queries) > 0 {
		f.lastKnown = queries[0].Known
	}
	return f.next(f.lastKnown), nil
}

// TestAPendingGateMakesSyncReadTheRepository is the chicken-and-egg the issue
// identified: nothing is waiting on the repository until the gate resolves,
// and the gate cannot resolve until the tags are read. The gate itself is what
// asks for them.
func TestAPendingGateMakesSyncReadTheRepository(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)

	a := store.NewAction("wait for a ref >=minor+2 in owner/repo", "wait_ref")
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}
	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Insert(ctx, a); err != nil {
		t.Fatalf("inserting the action: %v", err)
	}
	if err := tx.AddPendingRef(ctx, store.PendingRef{
		ActionID: a.ID, RepoID: "owner/repo", Kind: store.RefTag, Spec: ">=minor+2",
	}); err != nil {
		t.Fatalf("AddPendingRef() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	client := &fakeFetcher{refs: github.RefResult{Refs: []github.Ref{
		{Repo: "owner/repo", Prefix: "refs/tags/", Name: "v1.7.5", CommitSHA: "sha"},
		{Repo: "owner/repo", Prefix: "refs/tags/", Name: "v1.6.0", CommitSHA: "sha"},
	}}}

	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(client.askedRefs) != 1 {
		t.Fatalf("Sync() made %d ref reads for a pending gate, want 1", len(client.askedRefs))
	}
	if len(result.Resolved) != 1 {
		t.Fatalf("Sync() resolved %d gates, want 1", len(result.Resolved))
	}
	if got := result.Resolved[0].Wait.Matcher; got != ">=1.9.0" {
		t.Errorf("resolved to %q, want >=1.9.0", got)
	}

	// And it is a wait now, so the next poll asks about it for the ordinary
	// reason rather than for the gate's.
	waits, err := st.OpenRefWaits(ctx)
	if err != nil {
		t.Fatalf("OpenRefWaits() returned error: %v", err)
	}
	if len(waits) != 1 || waits[0].ActionID != a.ID {
		t.Errorf("OpenRefWaits() = %+v, want the resolved wait", waits)
	}
}

// TestSyncRecordsWhenAPullRequestMerged: merged_at is GitHub's timestamp, not
// the poll's. The week in review is ordered by it, and a pull request tracked
// after it merged has no transition in the log to order by instead.
func TestSyncRecordsWhenAPullRequestMerged(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)

	merged := observed(key)
	merged.State = "MERGED"
	merged.MergedAt = "2026-08-07T14:30:00Z"

	client := &fakeFetcher{result: github.Result{PullRequests: []github.PullRequest{merged}}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	pr := loadPR(t, st, key)
	if got := pr.MergedAt.String; got != "2026-08-07T14:30:00Z" {
		t.Errorf("merged_at = %q, want GitHub's timestamp", got)
	}

	// An open pull request reports no mergedAt, and that empty value must not
	// unset one — a merge does not come undone.
	reopened := observed(key)
	reopened.State = "OPEN"
	reopened.MergedAt = ""
	client.result = github.Result{PullRequests: []github.PullRequest{reopened}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if got := loadPR(t, st, key).MergedAt.String; got != "2026-08-07T14:30:00Z" {
		t.Errorf("merged_at = %q after a poll that said nothing, want it kept", got)
	}
}
