package store

import (
	"context"
	"database/sql"
	"testing"
)

func under(id string, parent string) *Project {
	p := &Project{ID: id}
	if parent != "" {
		p.ParentID = sql.NullString{String: parent, Valid: true}
	}
	return p
}

func shape(nodes []TreeNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Project.ID
		for d := 0; d < n.Depth; d++ {
			out[i] = ">" + out[i]
		}
	}
	return out
}

func same(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestTreeKeepsTheOrderItWasGiven: whatever --sort chose applies at every
// depth. A hierarchy is a way of reading a list, not a reranking of it.
func TestTreeKeepsTheOrderItWasGiven(t *testing.T) {
	// As a sort would hand them over: parents and children interleaved.
	nodes := Tree([]*Project{
		under("A", ""), under("C", "A"), under("B", "A"), under("D", ""),
	})
	if want := []string{"A", ">C", ">B", "D"}; !same(shape(nodes), want) {
		t.Errorf("Tree() = %v, want %v", shape(nodes), want)
	}
}

func TestTreeNests(t *testing.T) {
	nodes := Tree([]*Project{under("A", ""), under("B", "A"), under("C", "B")})
	if want := []string{"A", ">B", ">>C"}; !same(shape(nodes), want) {
		t.Errorf("Tree() = %v, want %v", shape(nodes), want)
	}
}

// TestAChildWhoseParentIsAbsentIsARoot: the list is usually filtered, and
// dropping a child because its parent did not match is the opposite of what
// filtering asked for.
func TestAChildWhoseParentIsAbsentIsARoot(t *testing.T) {
	nodes := Tree([]*Project{under("B", "A"), under("C", "")})
	if want := []string{"B", "C"}; !same(shape(nodes), want) {
		t.Errorf("Tree() = %v, want %v", shape(nodes), want)
	}
}

// TestTreeLosesNothingToACycle: nothing here can write one, but a database
// edited by hand could — and drawing a row oddly beats dropping it silently.
func TestTreeLosesNothingToACycle(t *testing.T) {
	nodes := Tree([]*Project{under("A", "B"), under("B", "A"), under("C", "")})
	if len(nodes) != 3 {
		t.Errorf("Tree() returned %v, want every project", shape(nodes))
	}
}

// TestWithAncestorsConnectsWhatWasAskedFor: a closed parent above open work is
// pulled back in, and marked, because it is context rather than something to
// do.
func TestWithAncestorsConnectsWhatWasAskedFor(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	top := addProjectRow(t, st, "Platform", "")
	middle := addProjectRow(t, st, "Storage", top)
	leaf := addProjectRow(t, st, "Sharding", middle)

	// As a filter would return it: only the leaf matched.
	one, err := st.projectsByID(ctx, []string{leaf})
	if err != nil {
		t.Fatalf("projectsByID() returned error: %v", err)
	}
	connected, err := st.WithAncestors(ctx, one)
	if err != nil {
		t.Fatalf("WithAncestors() returned error: %v", err)
	}
	if len(connected) != 3 {
		t.Fatalf("WithAncestors() returned %d projects, want the chain of 3", len(connected))
	}

	nodes := Tree(connected)
	if want := []string{top, ">" + middle, ">>" + leaf}; !same(shape(nodes), want) {
		t.Errorf("Tree() = %v, want %v", shape(nodes), want)
	}
	for _, n := range nodes {
		wantContext := n.Project.ID != leaf
		if n.Context != wantContext {
			t.Errorf("%s context = %v, want %v", n.Project.ID, n.Context, wantContext)
		}
	}
}

// TestWithAncestorsPullsInNothingExtra: a project with no open descendants is
// not an ancestor of anything asked for, so nothing reaches it. That is what
// makes a recursive count of open children unnecessary.
func TestWithAncestorsPullsInNothingExtra(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	top := addProjectRow(t, st, "Platform", "")
	addProjectRow(t, st, "Retired", top)
	other := addProjectRow(t, st, "Docs", "")

	one, err := st.projectsByID(ctx, []string{other})
	if err != nil {
		t.Fatalf("projectsByID() returned error: %v", err)
	}
	connected, err := st.WithAncestors(ctx, one)
	if err != nil {
		t.Fatalf("WithAncestors() returned error: %v", err)
	}
	if len(connected) != 1 {
		t.Errorf("WithAncestors() returned %d, want only what was asked for", len(connected))
	}
}

// TestSetParentRefusesACycle at every depth, since only the one-step version
// is something the schema can see.
func TestSetParentRefusesACycle(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	top := addProjectRow(t, st, "Platform", "")
	middle := addProjectRow(t, st, "Storage", top)
	leaf := addProjectRow(t, st, "Sharding", middle)
	// Written before the transaction opens: a reader inside one cannot see a
	// row committed by another after its snapshot was taken.
	docsID := addProjectRow(t, st, "Docs", "")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	p, err := tx.LoadProject(ctx, top)
	if err != nil {
		t.Fatalf("LoadProject() returned error: %v", err)
	}
	if err := tx.SetParent(ctx, p, leaf); err == nil {
		t.Error("a cycle two levels deep was accepted")
	}
	if err := tx.SetParent(ctx, p, top); err == nil {
		t.Error("a project was made its own parent")
	}
	// And a legitimate move is still allowed.
	docs, err := tx.LoadProject(ctx, docsID)
	if err != nil {
		t.Fatalf("LoadProject() returned error: %v", err)
	}
	if err := tx.SetParent(ctx, docs, leaf); err != nil {
		t.Errorf("SetParent() refused a move that makes no cycle: %v", err)
	}
}

// addProjectRow writes a project, optionally under another, and returns its id.
func addProjectRow(t *testing.T, st *Store, title, parent string) string {
	t.Helper()
	ctx := context.Background()

	p := NewProject(title)
	if err := st.AllocateProject(ctx, p); err != nil {
		t.Fatalf("AllocateProject() returned error: %v", err)
	}
	if parent != "" {
		p.ParentID = sql.NullString{String: parent, Valid: true}
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	if err := tx.Insert(ctx, p); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return p.ID
}
