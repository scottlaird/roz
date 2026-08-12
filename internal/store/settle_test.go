package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// pipelineFor closes a write action against a synced pull request and returns
// the chain it produced, which is the state every test here starts from.
func pipelineFor(t *testing.T, st *Store, pipeline string) (*PR, []*Action) {
	t.Helper()

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", pipeline)
	pr := trackPR(t, st, "scottlaird/roz", 1)
	write := addAction(t, st, "write the endpoint", "write")

	result := closeIt(t, st, CloseRequest{ID: write.ID, PR: pr.ID})
	return pr, result.Created
}

func settle(t *testing.T, st *Store) []Settled {
	t.Helper()

	settled, err := st.Settle(context.Background(), ActorPredicate)
	if err != nil {
		t.Fatalf("Settle() returned error: %v", err)
	}
	return settled
}

// TestSettleClosesTheStepGitHubFinished is the whole point: nobody says the
// pull request is out of draft, GitHub does, and the action goes away.
func TestSettleClosesTheStepGitHubFinished(t *testing.T) {
	st := newStore(t)
	pr, chain := pipelineFor(t, st, PipelineDirect)

	// Nothing is satisfied yet: an unsynced pull request closes nothing.
	if settled := settle(t, st); len(settled) != 0 {
		t.Fatalf("Settle() closed %v before anything was observed", settledIDs(settled))
	}

	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
	})

	settled := settle(t, st)
	if len(settled) != 1 || settled[0].Action.ID != chain[0].ID {
		t.Fatalf("Settle() closed %v, want [%s]", settledIDs(settled), chain[0].ID)
	}
	if settled[0].Action.State != ActionDone {
		t.Errorf("state = %q, want %q", settled[0].Action.State, ActionDone)
	}
	if settled[0].PR != pr.ID {
		t.Errorf("PR = %q, want %q", settled[0].PR, pr.ID)
	}
}

func settledIDs(settled []Settled) []string {
	out := make([]string, len(settled))
	for i, s := range settled {
		out[i] = s.Action.ID
	}
	return out
}

// TestSettleAdvancesTheChain: closing a step frees the next, and if that one
// is satisfied too it goes in the same pass. Merging a pull request that was
// already approved should not need three polls to catch up.
func TestSettleAdvancesTheChain(t *testing.T) {
	st := newStore(t)
	pr, chain := pipelineFor(t, st, PipelineDirect) // undraft → merge

	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
		p.State = sql.NullString{String: PRStateMerged, Valid: true}
	})

	settled := settle(t, st)
	if !equalStrings(settledIDs(settled), []string{chain[0].ID, chain[1].ID}) {
		t.Fatalf("Settle() closed %v, want the whole chain %v",
			settledIDs(settled), []string{chain[0].ID, chain[1].ID})
	}
}

// TestSettleFreesWhatTheStepBlocked: settling runs the same cascade a manual
// close does, so anything waiting behind a step is released.
func TestSettleFreesWhatTheStepBlocked(t *testing.T) {
	st := newStore(t)
	pr, chain := pipelineFor(t, st, PipelineDirect)

	waiting := addAction(t, st, "tell the team", "announce")
	blockOn(t, st, waiting, chain[0])

	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
	})

	settled := settle(t, st)
	if len(settled) != 1 {
		t.Fatalf("Settle() closed %v, want one step", settledIDs(settled))
	}
	// The next step of the chain is freed too, so what matters is that the
	// unrelated action waiting on the same step is among them.
	if !contains(ids(settled[0].Result.Unblocked), waiting.ID) {
		t.Errorf("freed %v, want it to include %s", ids(settled[0].Result.Unblocked), waiting.ID)
	}
	if got := stateOf(t, st, waiting.ID); got != ActionReady {
		t.Errorf("state = %q, want %q", got, ActionReady)
	}
}

// TestAbsenceIsNotCompletion: a pull request nobody has synced satisfies
// nothing, so an unreachable repository cannot empty the queue.
func TestAbsenceIsNotCompletion(t *testing.T) {
	st := newStore(t)
	_, chain := pipelineFor(t, st, PipelineReview)

	if settled := settle(t, st); len(settled) != 0 {
		t.Errorf("Settle() closed %v with nothing observed", settledIDs(settled))
	}
	for _, a := range chain {
		if got := stateOf(t, st, a.ID); got == ActionDone {
			t.Errorf("%s closed itself on no evidence", a.ID)
		}
	}
}

// TestSettleIgnoresHumanVerbs: a write action is not something GitHub can
// finish, however much it observes about the pull request.
func TestSettleIgnoresHumanVerbs(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/roz")
	pr := trackPR(t, st, "scottlaird/roz", 1)
	observe(t, st, pr, func(p *PR) {
		p.State = sql.NullString{String: PRStateMerged, Valid: true}
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
	})

	a := addAction(t, st, "review someone else's", "review")
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, pr.ID, RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if settled := settle(t, st); len(settled) != 0 {
		t.Errorf("Settle() closed %v, want nothing: review is human-closed", settledIDs(settled))
	}
}

// TestSettleIgnoresActionsWithNoSubject: a predicate verb with nothing to
// read cannot close, and that is not an error — it may be linked later.
func TestSettleIgnoresActionsWithNoSubject(t *testing.T) {
	st := newStore(t)
	addAction(t, st, "merge something, eventually", "merge")

	if settled := settle(t, st); len(settled) != 0 {
		t.Errorf("Settle() closed %v, want nothing", settledIDs(settled))
	}
}

// TestSettleClosesOutOfOrder: reality outranks the plan. A merged pull
// request means the merge happened, whatever the chain expected first.
func TestSettleClosesOutOfOrder(t *testing.T) {
	st := newStore(t)
	pr, chain := pipelineFor(t, st, PipelineReview)

	observe(t, st, pr, func(p *PR) {
		p.State = sql.NullString{String: PRStateMerged, Valid: true}
	})

	merge := chain[len(chain)-1]
	settled := settle(t, st)
	if !equalStrings(settledIDs(settled), []string{merge.ID}) {
		t.Fatalf("Settle() closed %v, want the merge step %s", settledIDs(settled), merge.ID)
	}
	if got := stateOf(t, st, chain[0].ID); got == ActionDone {
		t.Error("an unsatisfied step closed as well")
	}
}

// TestSettleIsLoggedAsPredicate: sync reports what GitHub says; the predicate
// decides what it means, and the log should not confuse the two.
func TestSettleIsLoggedAsPredicate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	pr, chain := pipelineFor(t, st, PipelineDirect)

	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
	})
	settle(t, st)

	events, err := st.Events(ctx, EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}

	var found bool
	for _, e := range events {
		if e.SubjectID == chain[0].ID && e.Field == "closed_reason" {
			found = true
			if e.Actor != string(ActorPredicate) {
				t.Errorf("actor = %q, want %q", e.Actor, ActorPredicate)
			}
		}
	}
	if !found {
		t.Errorf("no closing event for %s", chain[0].ID)
	}
}

// TestSyncActorsMayNotSettle: closing writes authored columns, so the actor
// that observed the fact is exactly the one that may not act on it.
func TestSyncActorsMayNotSettle(t *testing.T) {
	st := newStore(t)

	_, err := st.Settle(context.Background(), ActorSyncGitHub)
	if err == nil || !strings.Contains(err.Error(), "authored fields") {
		t.Errorf("error = %v, want sync refused", err)
	}
}

// TestSettleIsIdempotent: polling twice must not try to close what it already
// closed, which would fail rather than do nothing.
func TestSettleIsIdempotent(t *testing.T) {
	st := newStore(t)
	pr, _ := pipelineFor(t, st, PipelineDirect)

	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
	})

	if settled := settle(t, st); len(settled) == 0 {
		t.Fatal("nothing settled on the first pass")
	}
	if settled := settle(t, st); len(settled) != 0 {
		t.Errorf("the second pass closed %v, want nothing left", settledIDs(settled))
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestSettleActionIsScopedToTheOneNamed: a command that linked one row should
// not close every satisfied action on the pull request. That is sync's job,
// and doing it here would make a targeted command quietly a broad one.
func TestSettleActionIsScopedToTheOneNamed(t *testing.T) {
	st := newStore(t)
	pr, chain := pipelineFor(t, st, PipelineDirect) // undraft → merge

	// Both steps are satisfied, so a full Settle would close both.
	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
		p.State = sql.NullString{String: PRStateMerged, Valid: true}
	})

	settled, err := st.SettleAction(context.Background(), ActorPredicate, chain[1].ID)
	if err != nil {
		t.Fatalf("SettleAction() returned error: %v", err)
	}
	if !equalStrings(settledIDs(settled), []string{chain[1].ID}) {
		t.Errorf("SettleAction() closed %v, want only %s", settledIDs(settled), chain[1].ID)
	}

	// The other satisfied step is still open, waiting for a sync.
	ctx := context.Background()
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	still, err := tx.LoadAction(ctx, chain[0].ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if !still.IsOpen() {
		t.Errorf("%s was closed too, want it left for sync", chain[0].ID)
	}
}

// TestSettleActionFollowsWhatItFreed: what a closure unblocks is a consequence
// of the caller's act, so a freed successor that is already satisfied should
// not have to wait for a poll either.
func TestSettleActionFollowsWhatItFreed(t *testing.T) {
	st := newStore(t)
	pr, chain := pipelineFor(t, st, PipelineDirect) // undraft → merge, merge blocked

	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
		p.State = sql.NullString{String: PRStateMerged, Valid: true}
	})

	settled, err := st.SettleAction(context.Background(), ActorPredicate, chain[0].ID)
	if err != nil {
		t.Fatalf("SettleAction() returned error: %v", err)
	}
	if !equalStrings(settledIDs(settled), []string{chain[0].ID, chain[1].ID}) {
		t.Errorf("SettleAction() closed %v, want the step and what it freed %v",
			settledIDs(settled), []string{chain[0].ID, chain[1].ID})
	}
}

// TestSettleActionOnAnUnobservedPullRequest: absence is not completion, so
// this is a no-op rather than a verdict.
func TestSettleActionOnAnUnobservedPullRequest(t *testing.T) {
	st := newStore(t)
	_, chain := pipelineFor(t, st, PipelineDirect)

	settled, err := st.SettleAction(context.Background(), ActorPredicate, chain[0].ID)
	if err != nil {
		t.Fatalf("SettleAction() returned error: %v", err)
	}
	if len(settled) != 0 {
		t.Errorf("SettleAction() closed %v against an unsynced pull request", settledIDs(settled))
	}
}
