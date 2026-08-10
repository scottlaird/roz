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
	// Leave the caller holding what was committed, the way a command that
	// reloads before its next step would.
	*a = *after
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

// TestUnblockedIsTheQueue: what could be worked on right now is open, ready,
// and not folded out of sight behind something else.
func TestUnblockedIsTheQueue(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	ready := addAction(t, st, "do this one", "write")
	blocked := addAction(t, st, "waits on the first", "run")
	hidden := addAction(t, st, "nothing to do about it yet", "write")
	snoozed := addAction(t, st, "not until Thursday", "decide")
	closed := addAction(t, st, "already finished", "decide")

	blockOn(t, st, blocked, ready)
	attach(t, st, hidden, func(a *Action) {
		a.HiddenBehind = sql.NullString{String: ready.ID, Valid: true}
	})
	attach(t, st, snoozed, func(a *Action) {
		a.State = ActionSnoozed
		a.SnoozeUntil = sql.NullString{String: "2027-01-01", Valid: true}
	})
	closeIt(t, st, CloseRequest{ID: closed.ID})

	got, err := st.ListActions(ctx, ActionFilter{Unblocked: true})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if !equalStrings(ids(got), []string{ready.ID}) {
		t.Errorf("ListActions(unblocked) = %v, want [%s]", ids(got), ready.ID)
	}
}

// TestUnblockedFollowsTheCascade: closing the blocker puts the dependent in
// the queue, with nothing else asked to keep the two in step.
func TestUnblockedFollowsTheCascade(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocker := addAction(t, st, "do this one", "write")
	dependent := addAction(t, st, "waits on the first", "run")
	blockOn(t, st, dependent, blocker)

	closeIt(t, st, CloseRequest{ID: blocker.ID})

	got, err := st.ListActions(ctx, ActionFilter{Unblocked: true})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if !equalStrings(ids(got), []string{dependent.ID}) {
		t.Errorf("ListActions(unblocked) = %v, want [%s]", ids(got), dependent.ID)
	}
}

// TestPriorityOrderFollowsTheProject: an action inherits its urgency from
// what it advances, since it has no priority of its own.
func TestPriorityOrderFollowsTheProject(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	urgent := insertProject(t, st, "the urgent one")
	later := insertProject(t, st, "the later one")
	setPriority(t, st, urgent, 1)
	setPriority(t, st, later, 4)

	// Created in the wrong order on purpose.
	slow := addAction(t, st, "advance the later one", "write")
	quick := addAction(t, st, "advance the urgent one", "write")
	loose := addAction(t, st, "advances nothing", "write")
	attach(t, st, slow, func(a *Action) {
		a.ProjectID = sql.NullString{String: later.ID, Valid: true}
	})
	attach(t, st, quick, func(a *Action) {
		a.ProjectID = sql.NullString{String: urgent.ID, Valid: true}
	})

	got, err := st.ListActions(ctx, ActionFilter{Order: OrderPriority})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	// The one advancing nothing has no priority, so it sorts last.
	want := []string{quick.ID, slow.ID, loose.ID}
	if !equalStrings(ids(got), want) {
		t.Errorf("ListActions(priority) = %v, want %v", ids(got), want)
	}

	// The default is still creation order, which nothing else should have
	// quietly changed.
	got, err = st.ListActions(ctx, ActionFilter{})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if !equalStrings(ids(got), []string{slow.ID, quick.ID, loose.ID}) {
		t.Errorf("the default order changed: %v", ids(got))
	}
}

func setPriority(t *testing.T, st *Store, p *Project, priority int64) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.Priority = sql.NullInt64{Int64: priority, Valid: true}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	*p = *after
}

// TestRankPinOverridesPriority: rank_pin exists to override whatever the
// system worked out, so it has to win.
func TestRankPinOverridesPriority(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	urgent := insertProject(t, st, "the urgent one")
	setPriority(t, st, urgent, 1)

	high := addAction(t, st, "advance the urgent one", "write")
	attach(t, st, high, func(a *Action) {
		a.ProjectID = sql.NullString{String: urgent.ID, Valid: true}
	})
	pinned := addAction(t, st, "do this first, whatever the system thinks", "write")
	attach(t, st, pinned, func(a *Action) {
		a.RankPin = sql.NullInt64{Int64: 1, Valid: true}
	})

	got, err := st.ListActions(ctx, ActionFilter{Order: OrderPriority})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if !equalStrings(ids(got), []string{pinned.ID, high.ID}) {
		t.Errorf("ListActions(priority) = %v, want the pin first", ids(got))
	}
}

// TestPriorityOrderStillFilters: the join for the project's priority must not
// change which actions come back, only their order.
func TestPriorityOrderStillFilters(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	ready := addAction(t, st, "ready", "write")
	blocked := addAction(t, st, "blocked", "run")
	blockOn(t, st, blocked, ready)

	got, err := st.ListActions(ctx, ActionFilter{Unblocked: true, Order: OrderPriority})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if !equalStrings(ids(got), []string{ready.ID}) {
		t.Errorf("ListActions(unblocked, priority) = %v, want [%s]", ids(got), ready.ID)
	}
}

func TestProjectPriorityOrder(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	third := insertProject(t, st, "no priority at all")
	first := insertProject(t, st, "the urgent one")
	second := insertProject(t, st, "the middling one")
	setPriority(t, st, first, 1)
	setPriority(t, st, second, 3)

	got, err := st.ListProjects(ctx, ProjectFilter{Order: OrderPriority})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}

	var listed []string
	for _, p := range got {
		listed = append(listed, p.ID)
	}
	if !equalStrings(listed, []string{first.ID, second.ID, third.ID}) {
		t.Errorf("ListProjects(priority) = %v, want the unprioritised one last", listed)
	}
}
