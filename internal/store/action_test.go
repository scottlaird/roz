package store

import (
	"context"
	"database/sql"
	"testing"
)

// addAction creates and commits one action.
func addAction(t *testing.T, st *Store, title, verb string) *Action {
	t.Helper()
	ctx := context.Background()

	a := NewAction(title, verb)
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, a); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return a
}

func TestActionRoundTrips(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "write the endpoint", "write")

	if a.ID != "NA1" {
		t.Errorf("id = %q, want NA1", a.ID)
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
	if *loaded != *a {
		t.Errorf("loaded = %+v, want %+v", *loaded, *a)
	}
}

// TestActionNeedsAKnownVerb is the foreign key into the vocabulary doing its
// job: an action cannot use a verb that does not exist, so nothing can end up
// closing on a rule nobody wrote.
func TestActionNeedsAKnownVerb(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	a := NewAction("teleport somewhere", "teleport")
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, a); err == nil {
		t.Error("an action with an unknown verb was inserted, want a foreign key error")
	}
}

// TestAllocationOutsideATransaction covers the deadlock this hit in practice.
//
// Allocation writes on its own connection, by design. A read transaction left
// open across it cannot then upgrade to a writer — its snapshot is stale, and
// SQLite returns SQLITE_BUSY_SNAPSHOT rather than waiting for the timeout.
func TestAllocationOutsideATransaction(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Read first, and finish before allocating.
	read, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if _, err := read.LoadVerb(ctx, "write"); err != nil {
		t.Fatalf("LoadVerb() returned error: %v", err)
	}
	if err := read.Rollback(); err != nil {
		t.Fatalf("Rollback() returned error: %v", err)
	}

	a := NewAction("write the endpoint", "write")
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() after a closed read returned error: %v", err)
	}

	write, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer write.Rollback()

	if err := write.Insert(ctx, a); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := write.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func TestActionWaitingIsObserved(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "wait for review", "wait_review")

	human, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer human.Rollback()

	after := a.Clone()
	after.WaitingSince = sql.NullString{String: "2026-08-09T12:00:00.000Z", Valid: true}
	if _, err := human.Update(ctx, a, after); err == nil {
		t.Error("a human set waiting_since, want an error")
	}
}

// TestClosedAtIsCoupledToState is the schema's rule: an abandoned item must
// not be indistinguishable from a finished one, so closing sets both.
func TestClosedAtIsCoupledToState(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "write the endpoint", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	// State alone is refused.
	half := a.Clone()
	half.State = ActionDone
	if _, err := tx.Update(ctx, a, half); err == nil {
		t.Error("state moved to done without a closed_at, want the CHECK to refuse it")
	}

	// Both together are fine.
	whole := a.Clone()
	whole.State = ActionDone
	whole.ClosedAt = sql.NullString{String: "2026-08-09T12:00:00.000Z", Valid: true}
	whole.ClosedReason = sql.NullString{String: ClosedCompleted, Valid: true}
	if _, err := tx.Update(ctx, a, whole); err != nil {
		t.Errorf("closing properly returned error: %v", err)
	}
}

func TestActionCannotHideBehindItself(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "write the endpoint", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := a.Clone()
	after.HiddenBehind = sql.NullString{String: a.ID, Valid: true}
	if _, err := tx.Update(ctx, a, after); err == nil {
		t.Error("an action hid behind itself, want the CHECK to refuse it")
	}
}

func TestListActions(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	p := insertProject(t, st, "a project")

	write := addAction(t, st, "write it", "write")
	decide := addAction(t, st, "decide it", "decide")
	snoozed := addAction(t, st, "later", "investigate")

	// Attach one to the project and snooze another, in one pass each.
	attach(t, st, write, func(a *Action) { a.ProjectID = sql.NullString{String: p.ID, Valid: true} })
	attach(t, st, snoozed, func(a *Action) {
		a.State = ActionSnoozed
		a.SnoozeUntil = sql.NullString{String: "2000-01-01", Valid: true}
	})

	tests := []struct {
		name   string
		filter ActionFilter
		want   []string
	}{
		{name: "all, in creation order", filter: ActionFilter{},
			want: []string{write.ID, decide.ID, snoozed.ID}},
		{name: "by verb", filter: ActionFilter{Verb: "decide"}, want: []string{decide.ID}},
		{name: "by project", filter: ActionFilter{Project: p.ID}, want: []string{write.ID}},
		{name: "by state", filter: ActionFilter{State: ActionSnoozed}, want: []string{snoozed.ID}},
		{name: "expired", filter: ActionFilter{Expired: true}, want: []string{snoozed.ID}},
		{name: "open", filter: ActionFilter{Open: true},
			want: []string{write.ID, decide.ID, snoozed.ID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := st.ListActions(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListActions() returned error: %v", err)
			}
			ids := make([]string, len(got))
			for i, a := range got {
				ids[i] = a.ID
			}
			if !equalStrings(ids, tt.want) {
				t.Errorf("ListActions(%+v) = %v, want %v", tt.filter, ids, tt.want)
			}
		})
	}
}

// attach applies a change to an action and commits it.
func attach(t *testing.T, st *Store, a *Action, change func(*Action)) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := a.Clone()
	change(after)
	if _, err := tx.Update(ctx, a, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestListActionsOrdersByNumber: NA100 sorts before NA41 as text, which is
// why n exists.
func TestListActionsOrdersByNumber(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for range 10 {
		addAction(t, st, "an action", "write")
	}

	got, err := st.ListActions(ctx, ActionFilter{})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	want := []string{"NA1", "NA2", "NA3", "NA4", "NA5", "NA6", "NA7", "NA8", "NA9", "NA10"}
	ids := make([]string, len(got))
	for i, a := range got {
		ids[i] = a.ID
	}
	if !equalStrings(ids, want) {
		t.Errorf("ListActions() = %v, want %v", ids, want)
	}
}

func TestLoadSubjectResolvesActions(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "write it", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	subject, err := tx.LoadSubject(ctx, st, a.ID)
	if err != nil {
		t.Fatalf("LoadSubject() returned error: %v", err)
	}
	if subject.subjectType() != "action" || subject.subjectID() != a.ID {
		t.Errorf("LoadSubject() = %s/%s, want action/%s",
			subject.subjectType(), subject.subjectID(), a.ID)
	}
}
