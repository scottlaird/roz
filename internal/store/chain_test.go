package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// handMade sets up scottlaird/roz#96 exactly: a tracked pull request against a
// repository with a pipeline, and an action added by hand carrying one of that
// pipeline's verbs. Nothing about it is irregular, which is the problem.
func handMade(t *testing.T, st *Store, verb string) (*PR, *Action) {
	t.Helper()

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)
	a := addAction(t, st, "do the thing", verb)
	linkSubject(t, st, a, pr.ID)
	return pr, a
}

func linkSubject(t *testing.T, st *Store, a *Action, prID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.LinkPR(ctx, a, prID, RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func chainOf(t *testing.T, st *Store, prID string) *ChainState {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	state, err := tx.ChainOf(ctx, prID)
	if err != nil {
		t.Fatalf("ChainOf() returned error: %v", err)
	}
	return state
}

func instantiate(t *testing.T, st *Store, prID string) *ChainResult {
	t.Helper()

	result, err := st.InstantiateChain(context.Background(), ActorHuman, prID)
	if err != nil {
		t.Fatalf("InstantiateChain(%s) returned error: %v", prID, err)
	}
	return result
}

func missingVerbs(state *ChainState) []string {
	missing := state.Missing()
	verbs := make([]string, len(missing))
	for i, s := range missing {
		verbs[i] = s.Verb
	}
	return verbs
}

// TestAHandMadeHeadLeavesNothingBehindIt is the bug in #96, stated. Closing it
// is not a failure of anything — the predicate fired, the action closed for the
// right reason — and the pull request is left with no open actions at all,
// at the moment it stops being the author's problem.
func TestAHandMadeHeadLeavesNothingBehindIt(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	pr, head := handMade(t, st, "undraft")

	result := closeIt(t, st, CloseRequest{ID: head.ID})
	if len(result.Created) != 0 {
		t.Fatalf("closing a hand-made head created %v; this test is about it creating nothing",
			result.Created)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	open, err := tx.LinkedActions(ctx, pr.ID)
	if err != nil {
		t.Fatalf("LinkedActions() returned error: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open actions = %v, want none: that is the failure", IDs(open))
	}

	// And that state is visible rather than having to be deduced from an
	// empty queue, which is requirement 2.
	state := chainOf(t, st, pr.ID)
	want := []string{"send_for_review", "wait_review", "merge"}
	if got := missingVerbs(state); !equalStrings(got, want) {
		t.Errorf("missing = %v, want %v", got, want)
	}
}

// TestTheSummarySeparatesInheritedFromAbsent is the ambiguity #96 names: the
// pipeline column holds the override, so it reads the same for a pull request
// carried by its repository's chain and for one carried by nothing.
func TestTheSummarySeparatesInheritedFromAbsent(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/roz")
	unchained := trackPR(t, st, "scottlaird/roz", 1)
	if got := chainOf(t, st, unchained.ID).Summary(); got != "none" {
		t.Errorf("summary with no pipeline anywhere = %q, want %q", got, "none")
	}

	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	inherited := trackPR(t, st, "scottlaird/roz", 2)
	got := chainOf(t, st, inherited.ID).Summary()
	if !strings.HasPrefix(got, PipelineReview+" from scottlaird/roz") {
		t.Errorf("summary = %q, want it to name the pipeline and the repository it came from", got)
	}
}

// TestTheOverrideNamesItself: a pull request with its own pipeline is not
// inheriting anything, and saying it came from the repository would be wrong.
func TestTheOverrideNamesItself(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := pr.Clone()
	after.Pipeline = sql.NullString{String: PipelineDirect, Valid: true}
	if _, err := tx.Update(ctx, pr, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	state := chainOf(t, st, pr.ID)
	if state.Pipeline != PipelineDirect || state.Source != pr.ID {
		t.Errorf("chain = %q from %q, want %q from %s",
			state.Pipeline, state.Source, PipelineDirect, pr.ID)
	}
}

// TestInstantiateCreatesTheTail is the repair: the steps that never happened,
// in order, chained to each other.
func TestInstantiateCreatesTheTail(t *testing.T) {
	st := newStore(t)
	pr, head := handMade(t, st, "undraft")
	closeIt(t, st, CloseRequest{ID: head.ID})

	result := instantiate(t, st, pr.ID)
	if len(result.Created) != 3 {
		t.Fatalf("created %v, want the three remaining steps", IDs(result.Created))
	}
	for i, want := range []string{"send_for_review", "wait_review", "merge"} {
		if result.Created[i].Verb != want {
			t.Errorf("step %d is %q, want %q", i, result.Created[i].Verb, want)
		}
	}
	// The first is ready — what came before it is closed, and a closed action
	// frees nothing, so blocking behind it would be a wait that never ends.
	if result.BlockedBy[0] != "" {
		t.Errorf("the first step waits on %q, want nothing: what precedes it is closed",
			result.BlockedBy[0])
	}
	if result.BlockedBy[1] != result.Created[0].ID || result.BlockedBy[2] != result.Created[1].ID {
		t.Errorf("blockers = %v, want each step behind the one before it", result.BlockedBy)
	}
	if got := result.Created[0].State; got != ActionReady {
		t.Errorf("the first step is %q, want %q", got, ActionReady)
	}
	if got := result.Created[1].State; got != ActionBlocked {
		t.Errorf("the second step is %q, want %q", got, ActionBlocked)
	}
}

// TestANewStepWaitsOnTheStepBeforeIt, and not merely on the last thing created
// beside it. A pipeline missing its ends around an open middle — undraft and
// merge around a live wait_review — must not free merge when the review is
// still outstanding.
func TestANewStepWaitsOnTheStepBeforeIt(t *testing.T) {
	st := newStore(t)
	pr, wait := handMade(t, st, "wait_review")

	result := instantiate(t, st, pr.ID)
	if len(result.Created) != 3 {
		t.Fatalf("created %v, want undraft, send_for_review and merge", IDs(result.Created))
	}
	merge := len(result.Created) - 1
	if result.Created[merge].Verb != "merge" {
		t.Fatalf("last created is %q, want merge", result.Created[merge].Verb)
	}
	if got := result.BlockedBy[merge]; got != wait.ID {
		t.Errorf("merge waits on %q, want %s — the open review step it actually follows", got, wait.ID)
	}
}

// TestAnExistingActionIsNeverRearranged is the "do not adopt loose actions"
// caveat: somebody who added one merge step for a pull request they are
// lightly tracking asked for one item, and this may complete the chain around
// it without moving it.
func TestAnExistingActionIsNeverRearranged(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	pr, merge := handMade(t, st, "merge")

	instantiate(t, st, pr.ID)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	blockers, err := tx.OpenBlockers(ctx, merge.ID)
	if err != nil {
		t.Fatalf("OpenBlockers() returned error: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("%s gained blockers %v; an action that already existed is left alone",
			merge.ID, blockers)
	}
	reloaded, err := tx.LoadAction(ctx, merge.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if reloaded.State != ActionReady {
		t.Errorf("%s is %q, want it left %q", merge.ID, reloaded.State, ActionReady)
	}
}

// TestInstantiateIsIdempotent: running it twice must not yield two merge
// actions, which is requirement 3.
func TestInstantiateIsIdempotent(t *testing.T) {
	st := newStore(t)
	pr, head := handMade(t, st, "undraft")
	closeIt(t, st, CloseRequest{ID: head.ID})

	first := instantiate(t, st, pr.ID)
	second := instantiate(t, st, pr.ID)

	if len(second.Created) != 0 {
		t.Errorf("the second run created %v, want nothing", IDs(second.Created))
	}
	if got := chainOf(t, st, pr.ID); len(got.Missing()) != 0 {
		t.Errorf("still missing %v after instantiating", missingVerbs(got))
	}
	if len(first.Created) == 0 {
		t.Fatal("the first run created nothing; this test proves nothing")
	}
}

// TestClosedStepsAreNotRecreated, whatever they closed as. Writing the
// already-done steps back would put closes in the log that nobody performed;
// re-creating a dropped one would overrule a decision with a rule.
func TestClosedStepsAreNotRecreated(t *testing.T) {
	for _, reason := range []string{ClosedCompleted, ClosedDropped} {
		t.Run(reason, func(t *testing.T) {
			st := newStore(t)
			pr, head := handMade(t, st, "send_for_review")
			closeIt(t, st, CloseRequest{ID: head.ID, Reason: reason})

			result := instantiate(t, st, pr.ID)
			for _, a := range result.Created {
				if a.Verb == "send_for_review" {
					t.Errorf("recreated send_for_review as %s, which closed as %s", a.ID, reason)
				}
			}
		})
	}
}

// TestAStepTheRequestAlreadySatisfiesIsNotCreated, the rule readPipeline
// applies when the cascade instantiates: where pull requests are not created
// as drafts there is nothing to undraft, and an action complete before it
// exists is noise.
func TestAStepTheRequestAlreadySatisfiesIsNotCreated(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)
	observe(t, st, pr, func(p *PR) {
		p.IsDraft = sql.NullBool{Bool: false, Valid: true}
	})

	result := instantiate(t, st, pr.ID)
	for _, a := range result.Created {
		if a.Verb == "undraft" {
			t.Errorf("created %s to undraft a pull request that is not a draft", a.ID)
		}
	}
	if len(result.Created) == 0 {
		t.Fatal("created nothing at all; the rest of the chain was still wanted")
	}
}

// TestAnUnsyncedRequestGetsTheWholeChain is the other half of that rule, and
// the more important half: absence is not completion. A pull request nobody
// has read satisfies nothing.
func TestAnUnsyncedRequestGetsTheWholeChain(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/roz")
	setPipeline(t, st, "scottlaird/roz", PipelineReview)
	pr := trackPR(t, st, "scottlaird/roz", 1)

	result := instantiate(t, st, pr.ID)
	if len(result.Created) != 4 {
		t.Errorf("created %v, want all four steps of %s", IDs(result.Created), PipelineReview)
	}
}

// TestNoPipelineIsRefusedRatherThanSilent. Nothing to instantiate is a real
// state — a repository whose pull requests are not chained — so the command
// says which knob would change it rather than reporting success over nothing.
func TestNoPipelineIsRefusedRatherThanSilent(t *testing.T) {
	st := newStore(t)

	trackRepo(t, st, "scottlaird/roz")
	pr := trackPR(t, st, "scottlaird/roz", 1)

	_, err := st.InstantiateChain(context.Background(), ActorHuman, pr.ID)
	if err == nil {
		t.Fatal("InstantiateChain() succeeded with no pipeline anywhere, want an error")
	}
	if !strings.Contains(err.Error(), "--pipeline") {
		t.Errorf("error = %v, want it to name the flag that would fix it", err)
	}
}

// TestTheNewStepsJoinTheProjectTheChainIsIn. The cascade copies the project
// from the action it closed; there is no such action here, so the nearest
// truth is what the pull request's own steps already say. Without it a
// reconstructed chain lands in no project and is invisible on its page.
func TestTheNewStepsJoinTheProjectTheChainIsIn(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	pr, head := handMade(t, st, "undraft")

	project := addProject(t, st, "Rate-limit the public API")
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := head.Clone()
	after.ProjectID = sql.NullString{String: project.ID, Valid: true}
	if _, err := tx.Update(ctx, head, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	closeIt(t, st, CloseRequest{ID: head.ID})

	result := instantiate(t, st, pr.ID)
	if len(result.Created) == 0 {
		t.Fatal("created nothing")
	}
	for _, a := range result.Created {
		if a.ProjectID.String != project.ID {
			t.Errorf("%s is in project %q, want %s", a.ID, a.ProjectID.String, project.ID)
		}
	}
}

// setBecause records why a pull request is tracked, the way `pr track
// --because` does.
func setBecause(t *testing.T, st *Store, pr *PR, reason string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	after := before.Clone()
	after.TrackedBecause = sql.NullString{String: reason, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestAPipelineIsNotRunOnSomebodyElsesWork is #242. `repo track` fills in the
// default pipeline for any repository, which is right for one whose pull
// requests you write and wrong for one you are watching from outside — and the
// chain summary then reported three missing steps two lines above
// tracked_because saying the work was not yours.
func TestAPipelineIsNotRunOnSomebodyElsesWork(t *testing.T) {
	for _, reason := range []string{TrackedWatching, TrackedReviewing} {
		t.Run(reason, func(t *testing.T) {
			st := newStore(t)
			trackRepo(t, st, "spandigital/cel2sql")
			setPipeline(t, st, "spandigital/cel2sql", PipelineReview)
			pr := trackPR(t, st, "spandigital/cel2sql", 169)
			setBecause(t, st, pr, reason)

			state := chainOf(t, st, pr.ID)
			if state.Applies() {
				t.Errorf("a %s pull request runs a pipeline", reason)
			}
			if len(state.Missing()) != 0 {
				t.Errorf("missing = %v; none of them were ever wanted", missingVerbs(state))
			}
			// The pipeline is named rather than hidden: "none" alone would be
			// a second thing to work out when the repository plainly has one.
			summary := state.Summary()
			if !strings.Contains(summary, reason) || !strings.Contains(summary, PipelineReview) {
				t.Errorf("summary = %q, want it to name both %q and %q",
					summary, reason, PipelineReview)
			}
		})
	}
}

// TestInstantiatingSomebodyElsesChainIsRefused, rather than quietly building
// an undraft and a send_for_review against a pull request nobody here will
// ever undraft or announce.
func TestInstantiatingSomebodyElsesChainIsRefused(t *testing.T) {
	st := newStore(t)
	trackRepo(t, st, "spandigital/cel2sql")
	setPipeline(t, st, "spandigital/cel2sql", PipelineReview)
	pr := trackPR(t, st, "spandigital/cel2sql", 169)
	setBecause(t, st, pr, TrackedWatching)

	_, err := st.InstantiateChain(context.Background(), ActorHuman, pr.ID)
	if err == nil {
		t.Fatal("InstantiateChain() built a chain on a watched pull request")
	}
	for _, want := range []string{TrackedWatching, "--because"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// TestYourOwnWorkIsUnaffected, in both the stated and the unstated case.
// Unstated is by far the most rows, and reading it as somebody else's would be
// the same mistake in the other direction.
func TestYourOwnWorkIsUnaffected(t *testing.T) {
	for _, reason := range []string{TrackedAuthored, ""} {
		name := reason
		if name == "" {
			name = "unstated"
		}
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			trackRepo(t, st, "scottlaird/roz")
			setPipeline(t, st, "scottlaird/roz", PipelineReview)
			pr := trackPR(t, st, "scottlaird/roz", 1)
			if reason != "" {
				setBecause(t, st, pr, reason)
			}

			state := chainOf(t, st, pr.ID)
			if !state.Applies() {
				t.Fatalf("no pipeline applies to %s work", name)
			}
			if len(state.Missing()) == 0 {
				t.Errorf("nothing is missing on an untouched pull request")
			}
		})
	}
}
