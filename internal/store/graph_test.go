package store

import (
	"context"
	"database/sql"
	"testing"
)

// TestEdgesAreReadWhole, closed ends included. Who may draw what is the
// caller's question — a diagram about now hides a satisfied blocker, one
// explaining how the work got here shows it — and filtering here would settle
// that where there is least to go on.
func TestEdgesAreReadWhole(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocker := addAction(t, st, "the one in front", "write")
	blocked := addAction(t, st, "the one behind", "write")
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.AddBlocker(ctx, blocker, blocked); err != nil {
		t.Fatalf("AddBlocker() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	edges, err := st.ActionBlockEdges(ctx)
	if err != nil {
		t.Fatalf("ActionBlockEdges() returned error: %v", err)
	}
	if len(edges) != 1 || edges[0].From != blocker.ID || edges[0].To != blocked.ID {
		t.Fatalf("edges = %v, want one from %s to %s", edges, blocker.ID, blocked.ID)
	}

	// Closing the blocker does not remove the edge, and must not remove it
	// from this: the edge is the record of the dependency having existed.
	closeIt(t, st, CloseRequest{ID: blocker.ID})
	after, err := st.ActionBlockEdges(ctx)
	if err != nil {
		t.Fatalf("ActionBlockEdges() returned error: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("edges = %v after the blocker closed, want it kept", after)
	}
}

// TestHiddenBehindPointsForwards. The column names what an action is hidden
// behind, and an edge runs from the thing that has to happen first — so the
// two are opposite ways round and the loader is where that is settled.
func TestHiddenBehindPointsForwards(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	front := addAction(t, st, "the one in front", "write")
	behind := addAction(t, st, "tidy up after", "write")
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := behind.Clone()
	after.HiddenBehind = sql.NullString{String: front.ID, Valid: true}
	if _, err := tx.Update(ctx, behind, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	edges, err := st.HiddenBehindEdges(ctx)
	if err != nil {
		t.Fatalf("HiddenBehindEdges() returned error: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %v, want one", edges)
	}
	if edges[0].From != front.ID || edges[0].To != behind.ID {
		t.Errorf("edge = %s -> %s, want %s -> %s: the blocker comes first",
			edges[0].From, edges[0].To, front.ID, behind.ID)
	}
}

// TestNoEdgesIsNotAnError. Every one of these is empty on a fresh database,
// and a caller drawing a diagram has to be able to tell that apart from a
// failure without reading an error string.
func TestNoEdgesIsNotAnError(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for _, read := range []struct {
		name string
		fn   func(context.Context) ([]Edge, error)
	}{
		{"ProjectBlockEdges", st.ProjectBlockEdges},
		{"ActionBlockEdges", st.ActionBlockEdges},
		{"HiddenBehindEdges", st.HiddenBehindEdges},
		{"StackedOnEdges", st.StackedOnEdges},
	} {
		edges, err := read.fn(ctx)
		if err != nil {
			t.Errorf("%s() returned error: %v", read.name, err)
		}
		if len(edges) != 0 {
			t.Errorf("%s() = %v on an empty database", read.name, edges)
		}
	}
}
