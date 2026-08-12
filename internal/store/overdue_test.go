package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// waitingAction is a ready action on a verb that has an allowance, created at
// a chosen time so the clock can be wound forward without sleeping.
func waitingAction(t *testing.T, st *Store, verb, since string) *Action {
	t.Helper()
	ctx := context.Background()

	a := addAction(t, st, "wait for somebody", verb)
	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := a.Clone()
	after.WaitingSince = sql.NullString{String: since, Valid: true}
	if _, err := tx.Update(ctx, a, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return after
}

func at(t *testing.T, stamp string) time.Time {
	t.Helper()
	parsed, err := time.Parse(timeFormat, stamp)
	if err != nil {
		t.Fatalf("parsing %q: %v", stamp, err)
	}
	return parsed
}

// overdueNow winds the store's clock to a chosen moment and runs the pass.
// The clock is the store's so that the comparison and the exception it raises
// cannot disagree about what time it is.
func overdueNow(t *testing.T, st *Store, now string) []Overdue {
	t.Helper()
	st.now = func() time.Time { return at(t, now) }
	found, err := st.OverdueWaits(context.Background(), ActorPredicate)
	if err != nil {
		t.Fatalf("OverdueWaits() returned error: %v", err)
	}
	return found
}

// TestAWaitGoesOverdue is the whole point: excluding waits from the queue was
// right, and it left nothing speaking up when one went bad.
func TestAWaitGoesOverdue(t *testing.T) {
	st := newStore(t)
	// wait_review allows three days.
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	if found := overdueNow(t, st, "2026-08-03T00:00:00.000Z"); len(found) != 0 {
		t.Errorf("overdue after two days: %v", found)
	}

	found := overdueNow(t, st, "2026-08-05T00:00:00.000Z")
	if len(found) != 1 || found[0].Action.ID != a.ID {
		t.Fatalf("overdue = %v, want [%s] after four days", found, a.ID)
	}
	if found[0].Waiting != 4 {
		t.Errorf("waiting = %d days, want 4", found[0].Waiting)
	}
}

// TestOverdueRaisesAnException, rather than closing or snoozing it. What to
// do about a stuck wait is a judgement; this only says one is wanted.
func TestOverdueRaisesAnException(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	overdueNow(t, st, "2026-08-05T00:00:00.000Z")

	events, err := st.Events(ctx, EventQuery{Severity: SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("raised %d exceptions, want 1", len(events))
	}
	if events[0].Kind != EventWaitedTooLong || events[0].SubjectID != a.ID {
		t.Errorf("exception = %s on %s, want %s on %s",
			events[0].Kind, events[0].SubjectID, EventWaitedTooLong, a.ID)
	}

	// It stays open. Overdue is a report, not a state change.
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	reloaded, err := tx.LoadAction(ctx, a.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if !reloaded.IsOpen() || reloaded.State != ActionReady {
		t.Errorf("%s is %s, want it left alone", a.ID, reloaded.State)
	}
}

// TestOverdueIsReportedOnce: this runs on every sync, so a wait that stays
// bad must not raise an exception a minute.
func TestOverdueIsReportedOnce(t *testing.T) {
	st := newStore(t)
	waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	for _, now := range []string{
		"2026-08-05T00:00:00.000Z",
		"2026-08-05T00:01:00.000Z",
		"2026-08-09T00:00:00.000Z",
	} {
		overdueNow(t, st, now)
	}

	events, err := st.Events(context.Background(), EventQuery{Severity: SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("raised %d exceptions over three passes, want 1", len(events))
	}
}

// TestAFreshWaitIsReportedAgain: if the clock restarts — a new review was
// requested — the old exception falls before the new deadline, and this is a
// different wait rather than the same one.
func TestAFreshWaitIsReportedAgain(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	overdueNow(t, st, "2026-08-05T00:00:00.000Z")

	// Sync observes a later review request.
	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := a.Clone()
	after.WaitingSince = sql.NullString{String: "2026-08-06T00:00:00.000Z", Valid: true}
	if _, err := tx.Update(ctx, a, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	overdueNow(t, st, "2026-08-11T00:00:00.000Z")

	events, err := st.Events(ctx, EventQuery{Severity: SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("raised %d exceptions, want 2: a restarted wait is a new one", len(events))
	}
}

// TestAVerbWithNoAllowanceNeverTimesOut: a verb describing your own work
// cannot be overdue, only undone.
func TestAVerbWithNoAllowanceNeverTimesOut(t *testing.T) {
	st := newStore(t)
	waitingAction(t, st, "write", "2020-01-01T00:00:00.000Z")

	if found := overdueNow(t, st, "2026-08-11T00:00:00.000Z"); len(found) != 0 {
		t.Errorf("a write action went overdue after six years: %v", found)
	}
}

// TestOkayToWaitUntilOverridesTheVerb, in both directions: it is the one that
// is different, and different can mean longer as easily as shorter.
func TestOkayToWaitUntilOverridesTheVerb(t *testing.T) {
	for _, tt := range []struct {
		name    string
		until   string
		now     string
		overdue bool
	}{
		{"a longer leash", "2026-09-01T00:00:00.000Z", "2026-08-05T00:00:00.000Z", false},
		{"a shorter one", "2026-08-02T00:00:00.000Z", "2026-08-03T00:00:00.000Z", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newStore(t)
			ctx := context.Background()
			a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

			tx, err := st.Begin(ctx, ActorHuman)
			if err != nil {
				t.Fatalf("Begin() returned error: %v", err)
			}
			after := a.Clone()
			after.OkayToWaitUntil = sql.NullString{String: tt.until, Valid: true}
			if _, err := tx.Update(ctx, a, after); err != nil {
				t.Fatalf("Update() returned error: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("Commit() returned error: %v", err)
			}

			found := overdueNow(t, st, tt.now)
			if got := len(found) > 0; got != tt.overdue {
				t.Errorf("overdue = %v, want %v", got, tt.overdue)
			}
		})
	}
}

// TestAClosedWaitIsNotOverdue: closing is what settling does, and something
// already finished has nothing left to wait for.
func TestAClosedWaitIsNotOverdue(t *testing.T) {
	st := newStore(t)
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")
	closeIt(t, st, CloseRequest{ID: a.ID})

	if found := overdueNow(t, st, "2026-08-05T00:00:00.000Z"); len(found) != 0 {
		t.Errorf("a closed action went overdue: %v", found)
	}
}

// TestABlockedWaitIsNotOverdue: it is not waiting on a person yet, it is
// waiting on the step in front of it, and that one has its own clock.
func TestABlockedWaitIsNotOverdue(t *testing.T) {
	st := newStore(t)
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")
	blocker := addAction(t, st, "do this first", "write")
	blockOn(t, st, a, blocker)

	if found := overdueNow(t, st, "2026-08-05T00:00:00.000Z"); len(found) != 0 {
		t.Errorf("a blocked action went overdue: %v", found)
	}
}

// TestWaitingSinceFallsBackToCreation: nothing observed a wait beginning, so
// the best available answer is when the action appeared.
func TestWaitingSinceFallsBackToCreation(t *testing.T) {
	st := newStore(t)
	a := addAction(t, st, "wait for somebody", "wait_review")

	if a.WaitingSince.Valid {
		t.Fatal("a new action already has a waiting_since")
	}
	// newStore's clock starts in August 2026; four days on is overdue.
	found := overdueNow(t, st, "2026-08-20T00:00:00.000Z")
	if len(found) != 1 {
		t.Errorf("overdue = %v, want the action counted from its creation", found)
	}
}

// TestLoweredWaitDaysTakesEffect is the point of making the allowance
// editable: the number is read when the deadline is checked, so changing it
// changes what is overdue on the next pass rather than at the next release.
func TestLoweredWaitDaysTakesEffect(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	// Three days is the seeded allowance, so two days in is not yet overdue.
	if found := overdueNow(t, st, "2026-08-03T00:00:00.000Z"); len(found) != 0 {
		t.Fatalf("overdue after two days on the seeded allowance: %v", found)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	before, err := tx.LoadVerb(ctx, "wait_review")
	if err != nil {
		t.Fatalf("LoadVerb() returned error: %v", err)
	}
	after := before.Clone()
	after.WaitDays = sql.NullInt64{Int64: 1, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	found := overdueNow(t, st, "2026-08-03T00:00:00.000Z")
	if len(found) != 1 || found[0].Action.ID != a.ID {
		t.Errorf("overdue = %v, want [%s] once the allowance is one day", found, a.ID)
	}
}

// TestUpdateKeysOnTheVerb: every other record is keyed on id, and actionverb
// is not. An update that assumed id would fail against a column that is not
// there — or, worse on a table that had one, match the wrong row.
func TestUpdateKeysOnTheVerb(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	before, err := tx.LoadVerb(ctx, "merge")
	if err != nil {
		t.Fatalf("LoadVerb() returned error: %v", err)
	}
	after := before.Clone()
	after.WaitDays = sql.NullInt64{Int64: 7, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	read, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer read.Rollback()

	got, err := read.LoadVerb(ctx, "merge")
	if err != nil {
		t.Fatalf("LoadVerb() returned error: %v", err)
	}
	if !got.WaitDays.Valid || got.WaitDays.Int64 != 7 {
		t.Errorf("merge wait_days = %v, want 7", got.WaitDays)
	}
	// And only that row.
	other, err := read.LoadVerb(ctx, "wait_review")
	if err != nil {
		t.Fatalf("LoadVerb() returned error: %v", err)
	}
	if other.WaitDays.Int64 == 7 {
		t.Error("updating merge also changed wait_review")
	}
}

// TestAnOverdueWaitReachesTheQueue is the point of the whole thing: an
// exception is durable and queryable, and neither of those is a thing anybody
// does on a Monday morning. An action is simply there.
func TestAnOverdueWaitReachesTheQueue(t *testing.T) {
	st := newStore(t)
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	found := overdueNow(t, st, "2026-08-05T00:00:00.000Z")
	if len(found) != 1 {
		t.Fatalf("overdue = %v, want one", found)
	}
	if found[0].Raised == nil {
		t.Fatal("the overdue wait raised no action")
	}
	// Human-closed: "I looked and it is fine" has to be a way to close it, and
	// a predicate would refuse until the world changed.
	if got := found[0].Raised.Verb; got != RaiseVerb {
		t.Errorf("verb = %q, want %q", got, RaiseVerb)
	}
	if !strings.Contains(found[0].Raised.Why, a.ID) {
		t.Errorf("why = %q, want it to name the wait it is about", found[0].Raised.Why)
	}
}

// TestOneActionPerCondition: the rule may be made to re-report later, and the
// queue must not grow an item each time.
func TestOneActionPerCondition(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	first, err := st.RaiseAction(ctx, EventWaitedTooLong, a, "chase it", "because")
	if err != nil {
		t.Fatalf("RaiseAction() returned error: %v", err)
	}
	if first == nil {
		t.Fatal("the first raise produced nothing")
	}

	again, err := st.RaiseAction(ctx, EventWaitedTooLong, a, "chase it", "because")
	if err != nil {
		t.Fatalf("RaiseAction() returned error: %v", err)
	}
	if again != nil {
		t.Errorf("a second firing raised %s as well", again.Action.ID)
	}
}

// TestAClosedConditionStaysClosed. Some of these conditions persist and fire
// on every poll, so raising again once the action is closed would make the
// item impossible to clear — a nag with no off switch, which teaches people to
// ignore the queue.
func TestAClosedConditionStaysClosed(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	raised, err := st.RaiseAction(ctx, EventWaitedTooLong, a, "chase it", "because")
	if err != nil {
		t.Fatalf("RaiseAction() returned error: %v", err)
	}
	closeIt(t, st, CloseRequest{ID: raised.Action.ID})

	again, err := st.RaiseAction(ctx, EventWaitedTooLong, a, "chase it", "because")
	if err != nil {
		t.Fatalf("RaiseAction() returned error: %v", err)
	}
	if again != nil {
		t.Errorf("closing was undone by the next firing: raised %s", again.Action.ID)
	}
}

// TestDifferentConditionsRaiseSeparately: the key is the pair, so another
// exception about the same thing is a different item.
func TestDifferentConditionsRaiseSeparately(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := waitingAction(t, st, "wait_review", "2026-08-01T00:00:00.000Z")

	if _, err := st.RaiseAction(ctx, EventWaitedTooLong, a, "one", ""); err != nil {
		t.Fatalf("RaiseAction() returned error: %v", err)
	}
	other, err := st.RaiseAction(ctx, "something_else", a, "two", "")
	if err != nil {
		t.Fatalf("RaiseAction() returned error: %v", err)
	}
	if other == nil {
		t.Error("a different exception about the same subject raised nothing")
	}
}
