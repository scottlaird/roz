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

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)
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

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineDirect)
	pr := trackPR(t, st, "scottlaird/roz", 1)
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

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)
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

	trackRepo(t, st, "scottlaird/roz") // tracked directly, so pipeline is NULL
	pr := trackPR(t, st, "scottlaird/roz", 1)
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

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineDirect)
	pr := trackPR(t, st, "scottlaird/roz", 1)
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

	trackRepo(t, st, "scottlaird/roz")
	first := trackPR(t, st, "scottlaird/roz", 1)
	second := trackPR(t, st, "scottlaird/roz", 2)
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

// TestOnlyCompletingIsDone: three of the four reasons are abandonment, and
// the schema keeps state and closed_reason apart so an abandoned action
// cannot pass for a finished one.
func TestOnlyCompletingIsDone(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{ClosedCompleted, ActionDone},
		{ClosedDropped, ActionDropped},
		{ClosedObsolete, ActionDropped},
		{ClosedSuperseded, ActionDropped},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			st := newStore(t)
			a := addAction(t, st, "write it", "write")

			result := closeIt(t, st, CloseRequest{ID: a.ID, Reason: tt.reason})
			if result.Closed.State != tt.want {
				t.Errorf("closing as %s left state %q, want %q",
					tt.reason, result.Closed.State, tt.want)
			}
		})
	}
}

// TestAbandoningWorkInstantiatesNothing: dropping a write action must not
// produce the review chain that finishing it would have.
func TestAbandoningWorkInstantiatesNothing(t *testing.T) {
	st := newStore(t)
	pr, _ := func() (*PR, []*Action) {
		trackRepo(t, st, "scottlaird/roz")
		setPipeline(t, st, "scottlaird/roz", PipelineReview)
		return trackPR(t, st, "scottlaird/roz", 1), nil
	}()
	a := addAction(t, st, "write the endpoint", "write")

	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID, Reason: ClosedDropped})
	if len(result.Created) != 0 {
		t.Errorf("dropping the work created %v, want nothing", ids(result.Created))
	}
}

func closeProject(t *testing.T, st *Store, id, status string) *ProjectCloseResult {
	t.Helper()

	result, err := st.CloseProject(context.Background(), ActorHuman, id, status)
	if err != nil {
		t.Fatalf("CloseProject(%s, %s) returned error: %v", id, status, err)
	}
	return result
}

// TestCloseProjectDropsItsActions is the point of the verb: an action exists
// to advance a project, and a closed project cannot be advanced.
func TestCloseProjectDropsItsActions(t *testing.T) {
	st := newStore(t)
	p := insertProject(t, st, "Split the nodepool")

	first := addAction(t, st, "write it", "write")
	second := addAction(t, st, "roll it out", "run")
	elsewhere := addAction(t, st, "nothing to do with it", "announce")
	for _, a := range []*Action{first, second} {
		attach(t, st, a, func(a *Action) {
			a.ProjectID = sql.NullString{String: p.ID, Valid: true}
		})
	}

	result := closeProject(t, st, p.ID, ProjectRetired)
	if result.Project.Status != ProjectRetired {
		t.Errorf("status = %q, want %q", result.Project.Status, ProjectRetired)
	}
	if !equalStrings(ids(result.Dropped), []string{first.ID, second.ID}) {
		t.Fatalf("dropped %v, want both of the project's actions", ids(result.Dropped))
	}

	for _, a := range result.Dropped {
		if a.State != ActionDropped || a.ClosedReason.String != ClosedObsolete {
			t.Errorf("%s = %s/%s, want dropped/obsolete", a.ID, a.State, a.ClosedReason.String)
		}
	}
	if got := stateOf(t, st, elsewhere.ID); got != ActionReady {
		t.Errorf("an action on another project became %q", got)
	}
}

// TestCloseProjectFreesWhatItsActionsBlocked: dropping is still closing, so
// anything waiting behind one is released rather than left waiting on
// something that will never move.
func TestCloseProjectFreesWhatItsActionsBlocked(t *testing.T) {
	st := newStore(t)
	p := insertProject(t, st, "Split the nodepool")

	blocker := addAction(t, st, "write it", "write")
	attach(t, st, blocker, func(a *Action) {
		a.ProjectID = sql.NullString{String: p.ID, Valid: true}
	})
	waiting := addAction(t, st, "waits on it, but is not part of it", "announce")
	blockOn(t, st, waiting, blocker)

	result := closeProject(t, st, p.ID, ProjectDone)
	if !equalStrings(ids(result.Freed), []string{waiting.ID}) {
		t.Errorf("freed %v, want [%s]", ids(result.Freed), waiting.ID)
	}
	if got := stateOf(t, st, waiting.ID); got != ActionReady {
		t.Errorf("state = %q, want %q", got, ActionReady)
	}
}

// TestFreedThenDroppedIsNotReported: an action freed by one drop can be
// dropped by the next, and reporting it as freed would be a lie.
func TestFreedThenDroppedIsNotReported(t *testing.T) {
	st := newStore(t)
	p := insertProject(t, st, "Split the nodepool")

	blocker := addAction(t, st, "write it", "write")
	dependent := addAction(t, st, "roll it out", "run")
	for _, a := range []*Action{blocker, dependent} {
		attach(t, st, a, func(a *Action) {
			a.ProjectID = sql.NullString{String: p.ID, Valid: true}
		})
	}
	blockOn(t, st, dependent, blocker)

	result := closeProject(t, st, p.ID, ProjectRetired)
	if len(result.Freed) != 0 {
		t.Errorf("Freed = %v, want nothing: both were dropped", ids(result.Freed))
	}
	if !equalStrings(ids(result.Dropped), []string{blocker.ID, dependent.ID}) {
		t.Errorf("dropped %v, want both", ids(result.Dropped))
	}
}

func TestCloseProjectRejections(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "Split the nodepool")

	if _, err := st.CloseProject(ctx, ActorHuman, p.ID, ProjectSuperseded); err == nil ||
		!strings.Contains(err.Error(), "supersede") {
		t.Errorf("superseding through close returned %v, want it pointed at the verb", err)
	}
	if _, err := st.CloseProject(ctx, ActorHuman, p.ID, "abandoned"); err == nil {
		t.Error("an invented status was accepted, want an error")
	}

	closeProject(t, st, p.ID, ProjectDone)
	if _, err := st.CloseProject(ctx, ActorHuman, p.ID, ProjectRetired); err == nil ||
		!strings.Contains(err.Error(), "already done") {
		t.Errorf("closing twice returned %v, want it refused", err)
	}
}

// TestCloseProjectIsOneCorrelation: the project and everything it abandoned
// are one decision, and the log should say so.
func TestCloseProjectIsOneCorrelation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "Split the nodepool")

	a := addAction(t, st, "write it", "write")
	attach(t, st, a, func(a *Action) {
		a.ProjectID = sql.NullString{String: p.ID, Valid: true}
	})

	before, err := st.Events(ctx, EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	closeProject(t, st, p.ID, ProjectDone)

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
}

// TestCloseProjectClearsASnooze: a snooze says when to look again, and there
// is no again.
func TestCloseProjectClearsASnooze(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "Split the nodepool")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	snoozed := p.Clone()
	snoozed.Status = ProjectSnoozed
	snoozed.SnoozeUntil = sql.NullString{String: "2027-01-01", Valid: true}
	if _, err := tx.Update(ctx, p, snoozed); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	result := closeProject(t, st, p.ID, ProjectDone)
	if result.Project.SnoozeUntil.Valid {
		t.Errorf("snooze_until = %v, want it cleared", result.Project.SnoozeUntil)
	}
}

// setPRPipeline states one pull request's own chain.
func setPRPipeline(t *testing.T, st *Store, id string, pipeline sql.NullString) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, id)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	after := before.Clone()
	after.Pipeline = pipeline
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func createdVerbs(created []*Action) []string {
	verbs := make([]string, len(created))
	for i, a := range created {
		verbs[i] = a.Verb
	}
	return verbs
}

// TestPRPipelineOverridesTheRepository is the whole of SL24: the repository
// says how pull requests normally reach merge, and this one does not.
func TestPRPipelineOverridesTheRepository(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)
	setPRPipeline(t, st, pr.ID, sql.NullString{String: PipelineDirect, Valid: true})
	a := addAction(t, st, "hotfix the endpoint", "write")

	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})

	if result.Pipeline != PipelineDirect {
		t.Errorf("Pipeline = %q, want %q", result.Pipeline, PipelineDirect)
	}
	// direct is undraft then merge; review would have added the two review
	// steps between them.
	if got := createdVerbs(result.Created); !equalStrings(got, []string{"undraft", "merge"}) {
		t.Errorf("created %v, want the direct chain", got)
	}
}

// TestPRWithoutAPipelineFollowsTheRepository: the ordinary case, and the one
// that must keep working.
func TestPRWithoutAPipelineFollowsTheRepository(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)
	a := addAction(t, st, "write the endpoint", "write")

	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})

	if result.Pipeline != PipelineReview {
		t.Errorf("Pipeline = %q, want the repository's %q", result.Pipeline, PipelineReview)
	}
}

// TestPRPipelineIsReadAtCloseNotAtTrack: unset defers to the repository *now*,
// so a repository whose policy changes carries the pull requests that never
// claimed an exception to it. Copying the value at track time would have
// frozen this one on the old chain.
func TestPRPipelineIsReadAtCloseNotAtTrack(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)

	// The policy changes after the pull request was tracked.
	setPipeline(t, st, "scottlaird/roz", PipelineDirect)

	a := addAction(t, st, "write the endpoint", "write")
	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})

	if result.Pipeline != PipelineDirect {
		t.Errorf("Pipeline = %q, want the repository's new %q", result.Pipeline, PipelineDirect)
	}
}

// TestClearingAPRPipelineReturnsItToTheRepository: the exception has to be
// revocable, or naming one is a decision nobody can take back.
func TestClearingAPRPipelineReturnsItToTheRepository(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)
	setPRPipeline(t, st, pr.ID, sql.NullString{String: PipelineDirect, Valid: true})
	setPRPipeline(t, st, pr.ID, sql.NullString{})

	a := addAction(t, st, "write the endpoint", "write")
	result := closeIt(t, st, CloseRequest{ID: a.ID, PR: pr.ID})

	if result.Pipeline != PipelineReview {
		t.Errorf("Pipeline = %q, want the repository's %q back", result.Pipeline, PipelineReview)
	}
}

// TestPRPipelineIsAuthored: it is a judgement about how this change should be
// handled, so sync must not be able to write it — the same rule that keeps a
// repository's pipeline out of sync's hands.
func TestPRPipelineIsAuthored(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/roz")
	pr := trackPR(t, st, "scottlaird/roz", 1)

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := pr.Clone()
	after.Pipeline = sql.NullString{String: PipelineDirect, Valid: true}
	if _, err := tx.Update(ctx, pr, after); err == nil {
		t.Error("sync wrote pr.pipeline, want the authored rule to refuse it")
	}
}

// loadAction reads one back, for a test asserting on how it closed.
func loadAction(t *testing.T, st *Store, id string) *Action {
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
	return a
}

// TestClosingAWaitStandsDownItsChase is scottlaird/roz#178. A chase is
// derived from another action's state, so it must not be able to outlive it:
// the review arrives, the wait closes, and the chase is still telling somebody
// to nudge about a pull request that has already been approved.
func TestClosingAWaitStandsDownItsChase(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	wait := addAction(t, st, "wait for review on owner/repo#123", "wait_review")
	raised, err := st.RaiseAction(ctx, EventWaitedTooLong, wait,
		"chase "+wait.ID, wait.ID+" has been waiting 4 days.")
	if err != nil {
		t.Fatalf("RaiseAction() returned error: %v", err)
	}
	if raised == nil {
		t.Fatal("RaiseAction() raised nothing")
	}

	result, err := st.CloseAction(ctx, ActorHuman, CloseRequest{ID: wait.ID})
	if err != nil {
		t.Fatalf("CloseAction() returned error: %v", err)
	}
	if len(result.StoodDown) != 1 || result.StoodDown[0].ID != raised.Action.ID {
		t.Fatalf("StoodDown = %v, want the chase", result.StoodDown)
	}

	chase := loadAction(t, st, raised.Action.ID)
	if chase.ClosedAt.String == "" {
		t.Error("the chase is still open after its wait closed")
	}
	// Obsolete rather than completed: it records a nudge that never happened,
	// and calling it completed would inflate any count of how often chasing
	// was needed.
	if got := chase.ClosedReason.String; got != ClosedObsolete {
		t.Errorf("the chase closed as %q, want %q", got, ClosedObsolete)
	}
	if chase.State != ActionDropped {
		t.Errorf("the chase is %q, want it not to pass for finished", chase.State)
	}
	// And what it carried is kept.
	if chase.Title == "" || chase.Why == "" {
		t.Errorf("closing the chase discarded what it said: %+v", chase)
	}
}

// TestAChaseStandsDownWhenTheWaitIsDroppedToo: the requirement covers the
// other ways a wait ends, not only the happy path.
func TestAChaseStandsDownWhenTheWaitIsDroppedToo(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	wait := addAction(t, st, "wait for review on owner/repo#123", "wait_review")
	raised, err := st.RaiseAction(ctx, EventWaitedTooLong, wait, "chase "+wait.ID, "waiting")
	if err != nil {
		t.Fatalf("RaiseAction() returned error: %v", err)
	}

	if _, err := st.CloseAction(ctx, ActorHuman,
		CloseRequest{ID: wait.ID, Reason: ClosedDropped}); err != nil {
		t.Fatalf("CloseAction() returned error: %v", err)
	}
	if got := loadAction(t, st, raised.Action.ID); got.ClosedAt.String == "" {
		t.Error("a dropped wait left its chase open")
	}
}
