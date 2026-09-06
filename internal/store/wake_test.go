package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// snoozeUntil defers an action, the way `action snooze` does.
func snoozeUntil(t *testing.T, st *Store, a *Action, until string) {
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
	after.State = ActionSnoozed
	after.SnoozeUntil = sql.NullString{String: until, Valid: true}
	after.SnoozeReason = "check back"
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestTheDateEndsTheWaitByWakingIt is #264's second requirement. A snoozed
// action is invisible to the overdue check on purpose — overdueQuery filters
// on state, because a deferred action is not late — so the date arriving
// cannot be an exception. It has to bring the action back into play.
func TestTheDateEndsTheWaitByWakingIt(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	a := addAction(t, st, "handed to the other team", "wait_issue")
	snoozeUntil(t, st, a, "2026-08-01T00:00:00.000Z")

	// Before the date, nothing happens.
	st.now = func() time.Time { return at(t, "2026-07-20T00:00:00.000Z") }
	if woken, err := st.WakeExpired(ctx, ActorPredicate); err != nil {
		t.Fatalf("WakeExpired() returned error: %v", err)
	} else if !woken.Empty() {
		t.Fatalf("woke %v before its date", woken)
	}

	st.now = func() time.Time { return at(t, "2026-08-02T00:00:00.000Z") }
	woken, err := st.WakeExpired(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("WakeExpired() returned error: %v", err)
	}
	if len(woken.Actions) != 1 || woken.Actions[0].ID != a.ID {
		t.Fatalf("woke %v, want [%s]", woken, a.ID)
	}

	got := loadAction(t, st, a.ID)
	if got.State != ActionReady {
		t.Errorf("state = %q, want %q: the deferral is over", got.State, ActionReady)
	}
	// The date and reason go with it, the way `action wake` clears them by
	// hand: the row stops being a deferral because the deferral is over.
	if got.SnoozeUntil.Valid || got.SnoozeReason != "" {
		t.Errorf("still carries a snooze: until=%v reason=%q",
			got.SnoozeUntil, got.SnoozeReason)
	}
	// And it is not woken twice.
	if again, err := st.WakeExpired(ctx, ActorPredicate); err != nil {
		t.Fatalf("WakeExpired() returned error: %v", err)
	} else if !again.Empty() {
		t.Errorf("woke %v a second time", again)
	}
}

// TestTheIssueEndsTheWaitWhateverStateItIsIn is the first requirement, and it
// already held: Settle has no state filter, so a snoozed wait_issue closes the
// moment its issue does. Pinned here because the whole shape depends on it.
func TestTheIssueEndsTheWaitWhateverStateItIsIn(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	a := addAction(t, st, "handed to the other team", "wait_issue")
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.WaitOnIssue(ctx, a.ID, TrackerJira, "PROJ-42"); err != nil {
		t.Fatalf("WaitOnIssue() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	snoozeUntil(t, st, a, "2099-01-01")

	observeJira(t, st, TrackerObservation{
		Key:      "PROJ-42",
		Status:   sql.NullString{String: "Done", Valid: true},
		ClosedAt: sql.NullString{String: "2026-08-01T00:00:00.000Z", Valid: true},
	})

	settled, err := st.Settle(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("Settle() returned error: %v", err)
	}
	if len(settled) != 1 || settled[0].Action.ID != a.ID {
		t.Fatalf("settled %v, want the snoozed wait", settled)
	}
	if got := loadAction(t, st, a.ID); got.State != ActionDone {
		t.Errorf("state = %q, want %q", got.State, ActionDone)
	}
}

// TestTheTwoEndingsAreDistinguishable is the third requirement, and the one
// the issue calls load-bearing: a wait that always ends the same way teaches
// you to ignore it, so which end arrived has to be readable.
func TestTheTwoEndingsAreDistinguishable(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	timedOut := addAction(t, st, "the timer ran out", "wait_issue")
	snoozeUntil(t, st, timedOut, "2026-08-01T00:00:00.000Z")
	st.now = func() time.Time { return at(t, "2026-08-02T00:00:00.000Z") }
	if _, err := st.WakeExpired(ctx, ActorPredicate); err != nil {
		t.Fatalf("WakeExpired() returned error: %v", err)
	}

	events, err := st.Events(ctx, EventQuery{Kind: EventSnoozeExpired})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) != 1 || events[0].SubjectID != timedOut.ID {
		t.Fatalf("events = %v, want one %s about %s", events, EventSnoozeExpired, timedOut.ID)
	}
	// Not an exception: a deferral reaching its date is the deferral working.
	if events[0].Severity == SeverityException {
		t.Errorf("a deferral reaching its date was reported as a problem")
	}
	// And it says what ran out — the date and the reason — so the log
	// carries the answer rather than requiring the row to be read alongside
	// it. The reason matters: the wake clears it, and this note is where a
	// reader learns why the item is back.
	for _, want := range []string{"2026-08-01", "check back"} {
		if !strings.Contains(events[0].Note, want) {
			t.Errorf("note = %q, want it to mention %q", events[0].Note, want)
		}
	}
}

// TestAWokenActionWithABlockerGoesBackToBlocked. Waking ends the deferral; it
// is not a claim that the work became actionable.
func TestAWokenActionWithABlockerGoesBackToBlocked(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocker := addAction(t, st, "do this first", "write")
	deferred := addAction(t, st, "then this", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.AddBlocker(ctx, blocker, deferred); err != nil {
		t.Fatalf("AddBlocker() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	snoozeUntil(t, st, deferred, "2026-08-01T00:00:00.000Z")

	st.now = func() time.Time { return at(t, "2026-08-02T00:00:00.000Z") }
	if _, err := st.WakeExpired(ctx, ActorPredicate); err != nil {
		t.Fatalf("WakeExpired() returned error: %v", err)
	}
	if got := loadAction(t, st, deferred.ID); got.State != ActionBlocked {
		t.Errorf("state = %q, want %q: something still blocks it", got.State, ActionBlocked)
	}
}

// TestAProjectSnoozeEndsTheSameWay: actions and projects used to follow
// opposite rules for the same situation — an expired action was woken, an
// expired project sat there until somebody noticed. One sweep, one rule.
func TestAProjectSnoozeEndsTheSameWay(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	plain := addProject(t, st, "deferred")
	held := addProject(t, st, "deferred and blocked")
	blocker := addProject(t, st, "still open")
	mustBlockProject(t, st, held, blocker)
	snoozeProject(t, st, plain, "2026-08-01T00:00:00.000Z")
	snoozeProject(t, st, reloadProject(t, st, held.ID), "2026-08-01T00:00:00.000Z")

	st.now = func() time.Time { return at(t, "2026-07-20T00:00:00.000Z") }
	if woken, err := st.WakeExpired(ctx, ActorPredicate); err != nil {
		t.Fatalf("WakeExpired() returned error: %v", err)
	} else if !woken.Empty() {
		t.Fatalf("woke %v before the date", woken)
	}

	st.now = func() time.Time { return at(t, "2026-08-02T00:00:00.000Z") }
	woken, err := st.WakeExpired(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("WakeExpired() returned error: %v", err)
	}
	if got, want := IDs(woken.Projects), []string{plain.ID, held.ID}; !equalStrings(got, want) {
		t.Fatalf("woke %v, want %v", got, want)
	}

	// The deferral is over for both; whether the work is actionable is a
	// separate question, and the blocker answers it for one of them.
	for _, tc := range []struct{ id, want string }{{plain.ID, ProjectActive}, {held.ID, ProjectBlocked}} {
		got := reloadProject(t, st, tc.id)
		if got.Status != tc.want {
			t.Errorf("%s status = %q, want %q", tc.id, got.Status, tc.want)
		}
		if got.SnoozeUntil.Valid || got.SnoozeReason != "" {
			t.Errorf("%s still carries a snooze: until=%v reason=%q",
				tc.id, got.SnoozeUntil, got.SnoozeReason)
		}
	}

	events, err := st.Events(ctx, EventQuery{Kind: EventSnoozeExpired})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d %s events, want 2", len(events), EventSnoozeExpired)
	}
	for _, e := range events {
		if !strings.Contains(e.Note, "check back") {
			t.Errorf("note for %s = %q, want the reason in it", e.SubjectID, e.Note)
		}
	}

	if again, err := st.WakeExpired(ctx, ActorPredicate); err != nil {
		t.Fatalf("WakeExpired() returned error: %v", err)
	} else if !again.Empty() {
		t.Errorf("woke %v a second time", again)
	}
}
