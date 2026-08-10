package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// closeIt closes an action as a human, failing the test if it will not.
func closeIt(t *testing.T, st *Store, req CloseRequest) *CloseResult {
	t.Helper()

	result, err := st.CloseAction(context.Background(), ActorHuman, req)
	if err != nil {
		t.Fatalf("CloseAction(%+v) returned error: %v", req, err)
	}
	return result
}

func TestCloseSetsTheCoupledColumns(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "decide the shape", "decide")

	result := closeIt(t, st, CloseRequest{ID: a.ID})
	if result.Closed.State != ActionDone {
		t.Errorf("state = %q, want %q", result.Closed.State, ActionDone)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	loaded, err := tx.LoadAction(ctx, a.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if !loaded.ClosedAt.Valid || loaded.ClosedReason.String != ClosedCompleted {
		t.Errorf("closed_at = %v, closed_reason = %v; want both set",
			loaded.ClosedAt, loaded.ClosedReason)
	}
}

// TestDroppedIsNotDone is the distinction the schema keeps two columns for:
// abandoning something must not look like finishing it.
func TestDroppedIsNotDone(t *testing.T) {
	st := newStore(t)
	a := addAction(t, st, "the thing nobody wants", "decide")

	result := closeIt(t, st, CloseRequest{ID: a.ID, Reason: ClosedDropped})
	if result.Closed.State != ActionDropped {
		t.Errorf("state = %q, want %q", result.Closed.State, ActionDropped)
	}
}

func TestCloseRejections(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "write it", "write")

	if _, err := st.CloseAction(ctx, ActorHuman, CloseRequest{ID: a.ID, Reason: "bored"}); err == nil {
		t.Error("an invented reason was accepted, want an error")
	}

	closeIt(t, st, CloseRequest{ID: a.ID})
	_, err := st.CloseAction(ctx, ActorHuman, CloseRequest{ID: a.ID})
	if err == nil || !strings.Contains(err.Error(), "already done") {
		t.Errorf("closing twice returned %v, want it refused", err)
	}
}

// TestCloseFreesDependents is half the cascade: what this action was holding
// up becomes ready, and only if nothing else holds it up.
func TestCloseFreesDependents(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	first := addAction(t, st, "first", "write")
	second := addAction(t, st, "second", "write")
	dependent := addAction(t, st, "the one waiting", "run")
	blockOn(t, st, dependent, first)
	blockOn(t, st, dependent, second)

	// One of two blockers closing is not enough.
	result := closeIt(t, st, CloseRequest{ID: first.ID})
	if len(result.Unblocked) != 0 {
		t.Errorf("Unblocked = %v after one of two blockers, want none", ids(result.Unblocked))
	}
	if got := stateOf(t, st, dependent.ID); got != ActionBlocked {
		t.Errorf("state = %q, want it still %q", got, ActionBlocked)
	}

	// The last one is.
	result = closeIt(t, st, CloseRequest{ID: second.ID})
	if len(result.Unblocked) != 1 || result.Unblocked[0].ID != dependent.ID {
		t.Fatalf("Unblocked = %v, want [%s]", ids(result.Unblocked), dependent.ID)
	}
	if got := stateOf(t, st, dependent.ID); got != ActionReady {
		t.Errorf("state = %q, want %q", got, ActionReady)
	}

	_ = ctx
}

func stateOf(t *testing.T, st *Store, id string) string {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	a, err := tx.LoadAction(ctx, id)
	if err != nil {
		t.Fatalf("LoadAction(%s) returned error: %v", id, err)
	}
	return a.State
}

// TestCloseUnhides is the other half: what was folded out of the queue behind
// this one belongs back in it.
func TestCloseUnhides(t *testing.T) {
	st := newStore(t)

	blocker := addAction(t, st, "the real work", "write")
	hidden := addAction(t, st, "tidy up after", "write")
	attach(t, st, hidden, func(a *Action) {
		a.HiddenBehind = sql.NullString{String: blocker.ID, Valid: true}
	})

	result := closeIt(t, st, CloseRequest{ID: blocker.ID})
	if len(result.Unhidden) != 1 || result.Unhidden[0].ID != hidden.ID {
		t.Fatalf("Unhidden = %v, want [%s]", ids(result.Unhidden), hidden.ID)
	}
	if result.Unhidden[0].HiddenBehind.Valid {
		t.Error("hidden_behind is still set")
	}
}

// TestClosingWriteInstantiatesThePipeline is the claim the whole design rests
// on: the chain that follows a piece of work is produced, not typed.
func TestClosingWriteInstantiatesThePipeline(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/todo")
	setPipeline(t, st, "scottlaird/todo", PipelineReview)
	pr := trackPR(t, st, "scottlaird/todo", 1)
	a := addAction(t, st, "write the endpoint", "write")

	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})

	if result.Pipeline != PipelineReview {
		t.Errorf("Pipeline = %q, want %q", result.Pipeline, PipelineReview)
	}
	var verbs []string
	for _, created := range result.Created {
		verbs = append(verbs, created.Verb)
	}
	// undraft is skipped: an unsynced pull request has no is_draft, and a
	// predicate is false where nothing was observed — so it is kept, not
	// skipped. The whole chain should be here.
	want := []string{"undraft", "send_for_review", "wait_review", "merge"}
	if !equalStrings(verbs, want) {
		t.Fatalf("created %v, want %v", verbs, want)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	// The first step is ready and the rest wait on the one before.
	if result.Created[0].State != ActionReady {
		t.Errorf("first step is %q, want %q", result.Created[0].State, ActionReady)
	}
	for i := 1; i < len(result.Created); i++ {
		if result.Created[i].State != ActionBlocked {
			t.Errorf("step %d is %q, want %q", i, result.Created[i].State, ActionBlocked)
		}
		blockers, err := tx.OpenBlockers(ctx, result.Created[i].ID)
		if err != nil {
			t.Fatalf("OpenBlockers() returned error: %v", err)
		}
		if !equalStrings(blockers, []string{result.Created[i-1].ID}) {
			t.Errorf("step %d waits on %v, want [%s]", i, blockers, result.Created[i-1].ID)
		}
	}

	// Every step is about the same pull request.
	for _, created := range result.Created {
		subject, ok, err := tx.SubjectPR(ctx, created.ID)
		if err != nil {
			t.Fatalf("SubjectPR() returned error: %v", err)
		}
		if !ok || subject != pr.ID {
			t.Errorf("%s is about %q, want %q", created.ID, subject, pr.ID)
		}
	}
}

// setPipeline states a repository's pipeline.
func setPipeline(t *testing.T, st *Store, repo, pipeline string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadGitHubRepo(ctx, repo)
	if err != nil {
		t.Fatalf("LoadGitHubRepo() returned error: %v", err)
	}
	after := before.Clone()
	after.Pipeline = sql.NullString{String: pipeline, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestSatisfiedStepsAreSkipped is the compromise for repositories where pull
// requests are not created as drafts: there is nothing to undraft, and an
// action that is complete before it exists is noise.
func TestSatisfiedStepsAreSkipped(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/todo")
	setPipeline(t, st, "scottlaird/todo", PipelineDirect)
	pr := trackPR(t, st, "scottlaird/todo", 1)
	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
	})
	a := addAction(t, st, "write the endpoint", "write")

	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})

	if !equalStrings(result.Skipped, []string{"undraft"}) {
		t.Errorf("Skipped = %v, want [undraft]", result.Skipped)
	}
	var verbs []string
	for _, created := range result.Created {
		verbs = append(verbs, created.Verb)
	}
	if !equalStrings(verbs, []string{"merge"}) {
		t.Errorf("created %v, want only [merge]", verbs)
	}
}

// observe writes what sync would have written.
func observe(t *testing.T, st *Store, pr *PR, change func(*PR)) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := pr.Clone()
	change(after)
	if _, err := tx.Update(ctx, pr, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	*pr = *after
}

// TestOnlyPipelineStartingVerbsInstantiate: having a subject pull request is
// not on its own a reason to open a chain. Investigating one ends when you
// know the answer.
func TestOnlyPipelineStartingVerbsInstantiate(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/todo")
	setPipeline(t, st, "scottlaird/todo", PipelineReview)
	pr := trackPR(t, st, "scottlaird/todo", 1)
	a := addAction(t, st, "work out what it does", "investigate")

	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})
	if len(result.Created) != 0 {
		t.Errorf("created %v, want nothing", ids(result.Created))
	}
}

// TestNoPipelineInstantiatesNothing: a repository nobody has said anything
// about produces no chain, rather than a guessed one.
func TestNoPipelineInstantiatesNothing(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/todo") // tracked directly, so pipeline is NULL
	pr := trackPR(t, st, "scottlaird/todo", 1)
	a := addAction(t, st, "write the endpoint", "write")

	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})
	if len(result.Created) != 0 || result.Pipeline != "" {
		t.Errorf("created %v for pipeline %q, want nothing",
			ids(result.Created), result.Pipeline)
	}
}

// TestCloseIsOneCorrelation: closing writes to several tables, and the log is
// only useful if it recovers as a single act.
func TestCloseIsOneCorrelation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/todo")
	setPipeline(t, st, "scottlaird/todo", PipelineDirect)
	pr := trackPR(t, st, "scottlaird/todo", 1)
	a := addAction(t, st, "write the endpoint", "write")
	dependent := addAction(t, st, "deploy it", "run")
	blockOn(t, st, dependent, a)

	before, err := st.Events(ctx, EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})

	after, err := st.Events(ctx, EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}

	correlations := map[string]bool{}
	for _, e := range after[len(before):] {
		correlations[e.Correlation] = true
	}
	if len(correlations) != 1 {
		t.Errorf("closing wrote %d correlation ids, want 1", len(correlations))
	}
	if len(after)-len(before) < 5 {
		t.Errorf("closing wrote %d events, want the whole cascade", len(after)-len(before))
	}
}

// TestLinkingSomethingElseAtCloseIsRefused: an action is about one pull
// request, and closing is not the place to change which.
func TestLinkingSomethingElseAtCloseIsRefused(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/todo")
	first := trackPR(t, st, "scottlaird/todo", 1)
	second := trackPR(t, st, "scottlaird/todo", 2)
	a := addAction(t, st, "write the endpoint", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, first.ID, RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	_, err = st.CloseAction(ctx, ActorHuman, CloseRequest{ID: a.ID, PR: second.ID})
	if err == nil || !strings.Contains(err.Error(), "already about") {
		t.Errorf("error = %v, want the existing subject named", err)
	}
}

func TestCloseAnUntrackedPR(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "write the endpoint", "write")

	_, err := st.CloseAction(ctx, ActorHuman, CloseRequest{ID: a.ID, PR: "nobody/nothing#9"})
	if err == nil || !strings.Contains(err.Error(), "not tracked") {
		t.Errorf("error = %v, want it to say the pull request is not tracked", err)
	}
}
