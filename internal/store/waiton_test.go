package store

import (
	"context"
	"database/sql"
	"testing"
)

// TestWaitOnIsAWaitWithNothingToPoll is #258. Every other wait verb is backed
// by a predicate over something observable; there was nothing for an outage,
// an unanswered question, or another team's work.
//
// Each of those got `investigate`, which misreports the item three ways: a
// session rank class offers it as work when there is none to do, no allowance
// means nothing notices a wait that has run long, and it reads as something
// the owner can advance — which is exactly what it is not.
func TestWaitOnIsAWaitWithNothingToPoll(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	v, err := tx.LoadVerb(ctx, "wait_on")
	if err != nil {
		t.Fatalf("wait_on is not in the seeded vocabulary: %v", err)
	}

	// Human-closed and predicateless, deliberately. Inventing something to
	// observe would make the verb narrower than the need and put roz in the
	// business of monitoring third parties.
	if v.Closes != ClosesHuman {
		t.Errorf("closes = %q, want %q: there is nothing to poll", v.Closes, ClosesHuman)
	}
	if v.PredicateKey.Valid {
		t.Errorf("predicate = %q, want none", v.PredicateKey.String)
	}
	// A wait, so the queue stops offering it and other work can hide behind it
	// without the parent claiming to be actionable.
	if v.RankClass != RankWait {
		t.Errorf("rank class = %q, want %q", v.RankClass, RankWait)
	}
	// An allowance, where wait_ref has none. Nothing will ever close this on
	// its own, so a wait that has run long matters more here, not less.
	if !v.WaitDays.Valid || v.WaitDays.Int64 <= 0 {
		t.Errorf("wait_days = %v, want an allowance", v.WaitDays)
	}
	// It requires nothing. That is the whole point, and why this cannot be
	// expressed by relabelling an existing verb.
	if v.RequiresPR || v.RequiresRef || v.RequiresOwner || v.RequiresIssue {
		t.Errorf("wait_on requires a subject: pr=%v ref=%v owner=%v issue=%v",
			v.RequiresPR, v.RequiresRef, v.RequiresOwner, v.RequiresIssue)
	}
}

// TestAWaitOnGoesOverdue is the requirement the allowance exists for, and the
// half a settings check cannot show: nothing closes this on its own, so if the
// clock did not run it would sit there for ever unnoticed.
func TestAWaitOnGoesOverdue(t *testing.T) {
	st := newStore(t)
	a := waitingAction(t, st, "wait_on", "2026-08-01T00:00:00.000Z")

	// Inside the allowance.
	if found := overdueNow(t, st, "2026-08-03T00:00:00.000Z"); len(found) != 0 {
		t.Errorf("overdue after two days: %v", found)
	}
	found := overdueNow(t, st, "2026-08-06T00:00:00.000Z")
	if len(found) != 1 || found[0].Action.ID != a.ID {
		t.Fatalf("overdue = %v, want [%s] after five days", found, a.ID)
	}
	// A wait is not in the queue, so being overdue has to raise something that
	// is — otherwise the allowance would say nothing anybody could see.
	if found[0].InQueue() {
		t.Errorf("wait_on reports as already in the queue, so nothing would be raised")
	}
	if found[0].Raised == nil {
		t.Errorf("nothing was raised about an overdue wait nobody can see")
	}
}

// TestClosingAWaitOnFreesWhatWasBehindIt, the same as any other close. The
// case it exists for is an outage stopping a chain, so the chain has to start
// again when the outage ends.
func TestClosingAWaitOnFreesWhatWasBehindIt(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	outage := addAction(t, st, "the host is down", "wait_on")
	work := addAction(t, st, "open the pull request", "write")

	// Inlined rather than sharing a helper: a `hide` in this package would
	// collide with the one on another branch in flight, and this is four
	// lines.
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := work.Clone()
	after.HiddenBehind = sql.NullString{String: outage.ID, Valid: true}
	if _, err := tx.Update(ctx, work, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if got := loadAction(t, st, work.ID); !got.HiddenBehind.Valid {
		t.Fatalf("%s is not hidden, so this test proves nothing", work.ID)
	}

	result := closeIt(t, st, CloseRequest{ID: outage.ID})
	if len(result.Unhidden) != 1 || result.Unhidden[0].ID != work.ID {
		t.Errorf("unhid %v, want [%s]", result.Unhidden, work.ID)
	}
	if got := loadAction(t, st, work.ID); got.HiddenBehind.Valid {
		t.Errorf("%s is still hidden behind a closed wait", work.ID)
	}
}
