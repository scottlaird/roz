package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// blockOn is the edge, committed.
func blockOn(t *testing.T, st *Store, blocked, blocker *Action) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.AddBlocker(ctx, blocker, blocked); err != nil {
		t.Fatalf("AddBlocker() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestAddBlockerBlocks: the edge and the state move together, so nothing has
// to remember to set both.
func TestAddBlockerBlocks(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocked := addAction(t, st, "deploy it", "run")
	blocker := addAction(t, st, "write it", "write")
	blockOn(t, st, blocked, blocker)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	loaded, err := tx.LoadAction(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if loaded.State != ActionBlocked {
		t.Errorf("state = %q, want %q", loaded.State, ActionBlocked)
	}

	blockers, err := tx.OpenBlockers(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("OpenBlockers() returned error: %v", err)
	}
	if !equalStrings(blockers, []string{blocker.ID}) {
		t.Errorf("OpenBlockers() = %v, want [%s]", blockers, blocker.ID)
	}
}

// TestSnoozeOutranksBlocking: a snooze is a decision about time, and being
// blocked as well does not change when it should come back.
func TestSnoozeOutranksBlocking(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocked := addAction(t, st, "deploy it", "run")
	blocker := addAction(t, st, "write it", "write")
	attach(t, st, blocked, func(a *Action) {
		a.State = ActionSnoozed
		a.SnoozeUntil = sql.NullString{String: "2027-01-01", Valid: true}
	})
	blockOn(t, st, blocked, blocker)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	loaded, err := tx.LoadAction(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if loaded.State != ActionSnoozed {
		t.Errorf("state = %q, want it left %q", loaded.State, ActionSnoozed)
	}
}

// TestBlockingCyclesAreRefused: a cycle is a pair that never unblocks, and
// the CHECK only catches the one-step case.
func TestBlockingCyclesAreRefused(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	first := addAction(t, st, "first", "write")
	second := addAction(t, st, "second", "write")
	third := addAction(t, st, "third", "write")

	blockOn(t, st, second, first) // first blocks second
	blockOn(t, st, third, second) // second blocks third

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	// third blocking first would close the loop.
	err = tx.AddBlocker(ctx, third, first)
	if err == nil {
		t.Fatal("a cycle was accepted, want an error")
	}
	if !strings.Contains(err.Error(), "never unblock") {
		t.Errorf("error = %v, want it to explain the cycle", err)
	}

	// The direct case too, which the CHECK would otherwise report as a
	// constraint violation.
	if err := tx.AddBlocker(ctx, second, first); err == nil {
		t.Error("a two-action cycle was accepted, want an error")
	}
}

func TestActionCannotBlockItself(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "write it", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.AddBlocker(ctx, a, a); err == nil {
		t.Error("an action blocked itself, want an error")
	}
}

// TestClosedBlockersDoNotBlock is what makes the cascade work: nothing has to
// remove an edge, because a closed blocker stops counting.
func TestClosedBlockersDoNotBlock(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocked := addAction(t, st, "deploy it", "run")
	blocker := addAction(t, st, "write it", "write")
	blockOn(t, st, blocked, blocker)

	attach(t, st, blocker, func(a *Action) {
		a.State = ActionDone
		a.ClosedAt = sql.NullString{String: "2026-08-09T12:00:00.000Z", Valid: true}
		a.ClosedReason = sql.NullString{String: ClosedCompleted, Valid: true}
	})

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	blockers, err := tx.OpenBlockers(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("OpenBlockers() returned error: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("OpenBlockers() = %v, want none", blockers)
	}
}

func TestDependentsAndHiding(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocker := addAction(t, st, "write it", "write")
	dependent := addAction(t, st, "deploy it", "run")
	hidden := addAction(t, st, "tidy up after", "write")
	blockOn(t, st, dependent, blocker)
	attach(t, st, hidden, func(a *Action) {
		a.HiddenBehind = sql.NullString{String: blocker.ID, Valid: true}
	})

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	dependents, err := tx.Dependents(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("Dependents() returned error: %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != dependent.ID {
		t.Errorf("Dependents() = %v, want [%s]", ids(dependents), dependent.ID)
	}

	hiding, err := tx.Hiding(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("Hiding() returned error: %v", err)
	}
	if len(hiding) != 1 || hiding[0].ID != hidden.ID {
		t.Errorf("Hiding() = %v, want [%s]", ids(hiding), hidden.ID)
	}
}

func ids(actions []*Action) []string {
	out := make([]string, len(actions))
	for i, a := range actions {
		out[i] = a.ID
	}
	return out
}

// TestOneSubjectPR is the partial unique index the sketch calls the most
// valuable line in the schema: an action covering two unrelated pull requests
// is not merely discouraged, it is unrepresentable.
func TestOneSubjectPR(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	a := addAction(t, st, "write it", "write")
	trackRepo(t, st, "scottlaird/todo")
	first := trackPR(t, st, "scottlaird/todo", 1)
	second := trackPR(t, st, "scottlaird/todo", 2)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.LinkPR(ctx, a, first.ID, RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, second.ID, RoleSubject); err == nil {
		t.Error("a second subject was accepted, want the partial index to refuse it")
	}

	// Context is unlimited, which is the point of having two roles.
	if err := tx.LinkPR(ctx, a, second.ID, RoleContext); err != nil {
		t.Errorf("linking a context pull request returned error: %v", err)
	}

	subject, ok, err := tx.SubjectPR(ctx, a.ID)
	if err != nil {
		t.Fatalf("SubjectPR() returned error: %v", err)
	}
	if !ok || subject != first.ID {
		t.Errorf("SubjectPR() = %q, %v; want %q, true", subject, ok, first.ID)
	}
}

func TestLinkPRRejectsAnUnknownRole(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := addAction(t, st, "write it", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.LinkPR(ctx, a, "scottlaird/todo#1", "vaguely related"); err == nil {
		t.Error("an unknown role was accepted, want an error")
	}
}

func TestLinkedActions(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/todo")
	pr := trackPR(t, st, "scottlaird/todo", 1)
	subject := addAction(t, st, "write it", "write")
	context_ := addAction(t, st, "read the other one", "investigate")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.LinkPR(ctx, subject, pr.ID, RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, context_, pr.ID, RoleContext); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}

	linked, err := tx.LinkedActions(ctx, pr.ID)
	if err != nil {
		t.Fatalf("LinkedActions() returned error: %v", err)
	}
	if len(linked) != 1 || linked[0].ID != subject.ID {
		t.Errorf("LinkedActions() = %v, want only the subject, %s", ids(linked), subject.ID)
	}
}

// TestEdgeEventsShareOneCorrelation: adding an edge writes two rows — the
// edge event and the state change — and the log has to read as one act.
func TestEdgeEventsShareOneCorrelation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocked := addAction(t, st, "deploy it", "run")
	blocker := addAction(t, st, "write it", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.AddBlocker(ctx, blocker, blocked); err != nil {
		t.Fatalf("AddBlocker() returned error: %v", err)
	}
	correlation := tx.Correlation()
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	events, err := st.Events(ctx, EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}

	var kinds []string
	for _, e := range events {
		if e.Correlation == correlation {
			kinds = append(kinds, e.Kind)
		}
	}
	if !equalStrings(kinds, []string{"blocked", "changed"}) {
		t.Errorf("the edge wrote %v, want a blocked event and the state change", kinds)
	}
}
