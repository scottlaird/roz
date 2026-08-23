package store

import (
	"context"
	"database/sql"
	"testing"
)

// TestAWaitOnAnUnrequestedReviewIsReported is scottlaird/roz#83: an action sat
// as wait_review for four days on a pull request nobody had announced. GitHub
// showed a review request and two teams, so from the outside it looked like an
// ordinary wait — the request was automatic, and roz held the disproof the
// whole time.
func TestAWaitOnAnUnrequestedReviewIsReported(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, created := pipelineFor(t, st, "review")

	var wait *Action
	for _, a := range created {
		if a.Verb == "wait_review" {
			wait = a
		}
	}
	if wait == nil {
		t.Fatalf("the fixture has no wait_review action")
	}

	// The chain has to reach it first. A blocked step is not waiting on
	// anybody, and asking whether a review was requested of a pull request
	// that has not been sent out is the bug in #265.
	reachTheWait(t, st, created, wait)

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 1 || reported[0].Action.ID != wait.ID {
		t.Fatalf("reported %+v, want the one wait nobody announced", reported)
	}

	// Once, not once per cycle: the condition stays true until somebody acts
	// on it, and a monitor that repeats itself is one people turn off.
	again, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("reported %d waits on the second sweep, want none", len(again))
	}
}

// TestAnAnnouncedWaitIsNotReported, which is the ordinary case and has to stay
// silent.
func TestAnAnnouncedWaitIsNotReported(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	pr, _ := pipelineFor(t, st, "review")
	announce(t, st, pr.ID)

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 0 {
		t.Errorf("an announced wait was reported: %+v", reported)
	}
}

// TestAPipelineWithoutAnAnnouncementStaysSilent. An empty announced_at is not
// always wrong: a chain with no announcement step never records one, and a
// team relying on GitHub's own notifications has not made a mistake.
func TestAPipelineWithoutAnAnnouncementStaysSilent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// A chain that waits for a review without asking for one first.
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	quiet := &Pipeline{Name: "quiet", Steps: []PipelineStep{
		{Verb: "wait_review"}, {Verb: "merge"},
	}}
	if err := tx.InsertPipeline(ctx, quiet, ""); err != nil {
		t.Fatalf("InsertPipeline() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	pipelineFor(t, st, "quiet")

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 0 {
		t.Errorf("a chain with no announcement step was reported: %+v", reported)
	}
}

// announce records what `roz pr announce` records, which is the only thing
// this check reads.
func announce(t *testing.T, st *Store, id string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSlackManual)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, id)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	after := before.Clone()
	after.AnnouncedAt = sql.NullString{String: "2026-08-16T09:30:00Z", Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// hide parks an action behind another, the way `action hide-behind` does.
func hide(t *testing.T, st *Store, a *Action, behind string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadAction(ctx, a.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	after := before.Clone()
	after.HiddenBehind = sql.NullString{String: behind, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// reachTheWait closes everything ahead of a step, which is what the cascade
// does as a chain runs. A wait is only asked about once it is ready — see the
// predicate's own comment — so a fixture that wants a report has to get there.
func reachTheWait(t *testing.T, st *Store, created []*Action, wait *Action) {
	t.Helper()
	for _, a := range created {
		if a.ID == wait.ID {
			break
		}
		closeIt(t, st, CloseRequest{ID: a.ID})
	}
	if got := loadAction(t, st, wait.ID); got.State != ActionReady {
		t.Fatalf("%s is %q after its chain closed, want ready", wait.ID, got.State)
	}
}

func waitStep(t *testing.T, created []*Action) *Action {
	t.Helper()
	for _, a := range created {
		if a.Verb == "wait_review" {
			return a
		}
	}
	t.Fatal("the fixture has no wait_review action")
	return nil
}

// TestAHiddenWaitIsNotReported is #262. The ordinary pipeline on a draft pull
// request is undraft, an announce hidden behind it, and a wait_review hidden
// behind that — and the wait raised "nobody was asked for this review" every
// sweep, for ever, about a draft with an announce step queued up to do exactly
// what the exception was asking for. Two of them fired every sweep for a week.
func TestAHiddenWaitIsNotReported(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, created := pipelineFor(t, st, "review")
	wait := waitStep(t, created)

	hide(t, st, wait, created[0].ID)

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 0 {
		t.Errorf("reported %+v about an action that is parked, not waiting", reported)
	}
}

// TestHidingDefersTheQuestionRatherThanAnsweringIt: the exception has to fire
// the moment the step becomes live, or hiding would be a way to silence it for
// good — and the case it catches is a pull request nobody was asked to review,
// which does not stop being true because the step was queued behind another.
func TestHidingDefersTheQuestionRatherThanAnsweringIt(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, created := pipelineFor(t, st, "review")
	wait := waitStep(t, created)

	// Reachable by the chain, so that hiding is the only thing keeping it
	// quiet and the test is about hiding rather than about blocking.
	reachTheWait(t, st, created, wait)
	hide(t, st, wait, created[0].ID)
	if reported, err := st.UnannouncedWaits(ctx, ActorPredicate); err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	} else if len(reported) != 0 {
		t.Fatalf("reported while hidden: %+v", reported)
	}

	// Un-hidden, which is what closing the step in front does.
	tx, err := st.Begin(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	before, err := tx.LoadAction(ctx, wait.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	after := before.Clone()
	after.HiddenBehind = sql.NullString{}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 1 || reported[0].Action.ID != wait.ID {
		t.Errorf("reported %+v once live, want the wait: hiding must defer, not cancel", reported)
	}
}

// TestABlockedWaitIsNotReported, which is the case that actually fires and
// the one #262 got wrong.
//
// That issue reported the steps as hidden_behind each other and I fixed
// hidden_behind. A pipeline holds its steps with action_blocks — instantiate
// calls AddBlocker and nothing else — so no chain step is ever hidden, and the
// fix silenced a shape that does not occur while leaving the one that does.
// This test asserted the wrong behaviour was right. See #265.
func TestABlockedWaitIsNotReported(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, created := pipelineFor(t, st, "review")
	wait := waitStep(t, created)

	reloaded := loadAction(t, st, wait.ID)
	if reloaded.State != ActionBlocked {
		t.Fatalf("%s is %q, so this test is not about a blocked step", wait.ID, reloaded.State)
	}
	if reloaded.HiddenBehind.Valid {
		t.Fatalf("%s is hidden, so this test is not about blocking alone", wait.ID)
	}

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 0 {
		t.Errorf("reported %+v about a step whose chain has not reached it: "+
			"the pull request has not been sent out, which is correct for a draft", reported)
	}
}

// TestTheAdviceWouldRecordSomethingFalse is why this is a bug rather than
// noise. `pr announce` on an unsent pull request writes down an announcement
// that never happened and starts the wait clock, so a later real one reads as
// a duplicate and every elapsed figure after it is wrong. An exception whose
// remediation is harmful must not be raised.
func TestTheAdviceWouldRecordSomethingFalse(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	pr, created := pipelineFor(t, st, "review")
	wait := waitStep(t, created)

	if got := loadAction(t, st, wait.ID); got.State == ActionReady {
		t.Fatalf("%s is ready, so the chain has reached it and the advice is sound", wait.ID)
	}
	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	for _, r := range reported {
		if r.PR == pr.ID {
			t.Errorf("advised announcing %s, which has not been sent out", pr.ID)
		}
	}
}

// TestItFiresOnceTheChainReachesIt. Suppressing is not latching: nothing is
// recorded while the step is unready, so a wait that is genuinely stalled at
// the head of a chain is still the finding this was written for.
func TestItFiresOnceTheChainReachesIt(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, created := pipelineFor(t, st, "review")
	wait := waitStep(t, created)

	if reported, err := st.UnannouncedWaits(ctx, ActorPredicate); err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	} else if len(reported) != 0 {
		t.Fatalf("reported before the chain reached it: %+v", reported)
	}

	// Everything ahead of it closes, which is what the cascade does.
	for _, a := range created {
		if a.ID == wait.ID {
			break
		}
		closeIt(t, st, CloseRequest{ID: a.ID})
	}
	if got := loadAction(t, st, wait.ID); got.State != ActionReady {
		t.Fatalf("%s is %q after its chain closed, want ready", wait.ID, got.State)
	}

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 1 || reported[0].Action.ID != wait.ID {
		t.Errorf("reported %+v once live, want the wait: suppressing must not latch", reported)
	}
}

// TestASnoozedWaitIsNotReported. Ready is the test rather than "not blocked",
// so a wait deliberately parked until a date is quiet for the same reason a
// blocked one is: nothing about it is worth somebody's attention today.
func TestASnoozedWaitIsNotReported(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, created := pipelineFor(t, st, "review")
	wait := waitStep(t, created)

	for _, a := range created {
		if a.ID == wait.ID {
			break
		}
		closeIt(t, st, CloseRequest{ID: a.ID})
	}
	if reported, _ := st.UnannouncedWaits(ctx, ActorPredicate); len(reported) != 1 {
		t.Fatalf("the fixture does not report before snoozing, so this proves nothing")
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	before := loadAction(t, st, wait.ID)
	after := before.Clone()
	after.State = ActionSnoozed
	after.SnoozeUntil = sql.NullString{String: "2099-01-01", Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	// A fresh sweep: the previous one recorded an exception, so this asks
	// whether a snoozed step would be found at all.
	if reported, err := st.UnannouncedWaits(ctx, ActorPredicate); err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	} else if len(reported) != 0 {
		t.Errorf("reported %+v about a step snoozed until 2099", reported)
	}
}
