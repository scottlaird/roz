package ghsync

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// fakeFetcher returns a fixed answer and records what it was asked for.
type fakeFetcher struct {
	result github.Result
	err    error
	asked  []string
}

func (f *fakeFetcher) Fetch(_ context.Context, keys []string) (github.Result, error) {
	f.asked = append(f.asked, keys...)
	if f.err != nil {
		return github.Result{}, f.err
	}
	return f.result, nil
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

	// waiting_since is observed, so a sync actor writes it — which is what
	// really does, from the pull request's first review request.
	observing, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	long := a.Clone()
	long.WaitingSince = sql.NullString{String: "2020-01-01T00:00:00.000Z", Valid: true}
	if _, err := observing.Update(ctx, a, long); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := observing.Commit(); err != nil {
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
