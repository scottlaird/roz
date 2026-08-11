package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func blockProject(t *testing.T, st *Store, blocked, blocker *Project) error {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.BlockProject(ctx, blocker, blocked); err != nil {
		return err
	}
	return tx.Commit()
}

func mustBlockProject(t *testing.T, st *Store, blocked, blocker *Project) {
	t.Helper()
	if err := blockProject(t, st, blocked, blocker); err != nil {
		t.Fatalf("BlockProject() returned error: %v", err)
	}
}

func reloadProject(t *testing.T, st *Store, id string) *Project {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	p, err := tx.LoadProject(ctx, id)
	if err != nil {
		t.Fatalf("LoadProject() returned error: %v", err)
	}
	return p
}

// TestBlockingAProjectSetsItsStatus: "blocked" was a status with no referent,
// settable by hand and meaning nothing. The edge and the status move together
// now, the way they always have for actions.
func TestBlockingAProjectSetsItsStatus(t *testing.T) {
	st := newStore(t)
	blocker := insertProject(t, st, "thread the flag through")
	blocked := insertProject(t, st, "serve MCP over HTTP")

	mustBlockProject(t, st, blocked, blocker)

	if got := reloadProject(t, st, blocked.ID); got.Status != ProjectBlocked {
		t.Errorf("%s is %s, want blocked", got.ID, got.Status)
	}
	if got := reloadProject(t, st, blocker.ID); got.Status != ProjectActive {
		t.Errorf("the blocker is %s, want it untouched", got.Status)
	}
}

// TestClosingABlockerFreesTheProject is the half that did not exist: closing
// an action ran a cascade, closing a project did not, so a blocked project
// stayed blocked for ever unless somebody remembered.
func TestClosingABlockerFreesTheProject(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	blocker := insertProject(t, st, "thread the flag through")
	blocked := insertProject(t, st, "serve MCP over HTTP")
	mustBlockProject(t, st, blocked, blocker)

	result, err := st.CloseProject(ctx, ActorHuman, blocker.ID, ProjectDone)
	if err != nil {
		t.Fatalf("CloseProject() returned error: %v", err)
	}

	if len(result.Unblocked) != 1 || result.Unblocked[0].ID != blocked.ID {
		t.Fatalf("Unblocked = %v, want [%s]", result.Unblocked, blocked.ID)
	}
	if got := reloadProject(t, st, blocked.ID); got.Status != ProjectActive {
		t.Errorf("%s is %s after its blocker closed, want active", got.ID, got.Status)
	}
}

// TestTheLastBlockerFreesIt: one of two closing is not enough, which is the
// property that makes the status trustworthy rather than approximate.
func TestTheLastBlockerFreesIt(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	first := insertProject(t, st, "first blocker")
	second := insertProject(t, st, "second blocker")
	blocked := insertProject(t, st, "waiting on both")
	mustBlockProject(t, st, blocked, first)
	mustBlockProject(t, st, blocked, second)

	if _, err := st.CloseProject(ctx, ActorHuman, first.ID, ProjectDone); err != nil {
		t.Fatalf("CloseProject() returned error: %v", err)
	}
	if got := reloadProject(t, st, blocked.ID); got.Status != ProjectBlocked {
		t.Errorf("%s is %s with one blocker left, want blocked", got.ID, got.Status)
	}

	if _, err := st.CloseProject(ctx, ActorHuman, second.ID, ProjectDone); err != nil {
		t.Fatalf("CloseProject() returned error: %v", err)
	}
	if got := reloadProject(t, st, blocked.ID); got.Status != ProjectActive {
		t.Errorf("%s is %s with no blockers left, want active", got.ID, got.Status)
	}
}

// TestUnblockingRemovesTheEdge: sometimes the dependency was wrong rather
// than satisfied, and saying so should not mean closing something unfinished.
func TestUnblockingRemovesTheEdge(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	blocker := insertProject(t, st, "thread the flag through")
	blocked := insertProject(t, st, "serve MCP over HTTP")
	mustBlockProject(t, st, blocked, blocker)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	current, err := tx.LoadProject(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("LoadProject() returned error: %v", err)
	}
	if err := tx.UnblockProject(ctx, blocker, current); err != nil {
		t.Fatalf("UnblockProject() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if got := reloadProject(t, st, blocked.ID); got.Status != ProjectActive {
		t.Errorf("%s is %s after unblocking, want active", got.ID, got.Status)
	}
	// The blocker is untouched: nothing about it was finished.
	if got := reloadProject(t, st, blocker.ID); got.Status != ProjectActive {
		t.Errorf("the blocker is %s, want it still active", got.Status)
	}
}

// TestACycleIsRefused, directly and through a third project. A pair that
// blocks each other never unblocks.
func TestACycleIsRefused(t *testing.T) {
	st := newStore(t)
	first := insertProject(t, st, "first")
	second := insertProject(t, st, "second")
	third := insertProject(t, st, "third")

	mustBlockProject(t, st, second, first)
	if err := blockProject(t, st, first, second); err == nil {
		t.Error("a direct cycle was accepted")
	}

	mustBlockProject(t, st, third, second)
	err := blockProject(t, st, first, third)
	if err == nil {
		t.Fatal("a cycle through a third project was accepted")
	}
	if !strings.Contains(err.Error(), "never unblock") {
		t.Errorf("error = %v", err)
	}
}

// TestAProjectCannotBlockItself, the same CHECK action_blocks carries.
func TestAProjectCannotBlockItself(t *testing.T) {
	st := newStore(t)
	p := insertProject(t, st, "on its own")

	if err := blockProject(t, st, p, p); err == nil {
		t.Error("a project blocked itself")
	}
}

// TestASnoozeOutranksTheGraph: a snooze is a decision about time, and the
// edge should not overwrite it. Same rule applyBlockedState follows.
func TestASnoozeOutranksTheGraph(t *testing.T) {
	st := newStore(t)
	blocker := insertProject(t, st, "thread the flag through")
	blocked := insertProject(t, st, "deferred on purpose")
	snoozeProject(t, st, blocked, "2099-01-01")

	mustBlockProject(t, st, blocked, blocker)

	if got := reloadProject(t, st, blocked.ID); got.Status != ProjectSnoozed {
		t.Errorf("%s is %s, want the snooze left alone", got.ID, got.Status)
	}
}

// TestBlockedProjectsReachJSON: the edge has to be visible in both output
// formats, which is what the relation declarations buy.
func TestBlockedProjectsReachJSON(t *testing.T) {
	st := newStore(t)
	blocker := insertProject(t, st, "thread the flag through")
	blocked := insertProject(t, st, "serve MCP over HTTP")
	mustBlockProject(t, st, blocked, blocker)

	object := marshalled(t, st, reloadProject(t, st, blocked.ID))
	if got := stringList(t, object["blocked_by"]); !equalStrings(got, []string{blocker.ID}) {
		t.Errorf("blocked_by = %v, want [%s]", got, blocker.ID)
	}

	other := marshalled(t, st, reloadProject(t, st, blocker.ID))
	if got := stringList(t, other["blocking"]); !equalStrings(got, []string{blocked.ID}) {
		t.Errorf("blocking = %v, want [%s]", got, blocked.ID)
	}
}

// TestABlockedProjectIsStillOpen: it is live work, and the page filters on
// this. Hiding blocked projects is how one sat unseen for a session.
func TestABlockedProjectIsStillOpen(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	blocker := insertProject(t, st, "thread the flag through")
	blocked := insertProject(t, st, "serve MCP over HTTP")
	mustBlockProject(t, st, blocked, blocker)

	found, err := st.ListProjects(ctx, ProjectFilter{Open: true})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	if !equalStrings(projectIDs(found), []string{blocker.ID, blocked.ID}) {
		t.Errorf("open projects = %v, want both", projectIDs(found))
	}
}

// TestASnoozedActionIsNotWokenByTheGraph is the action half of the same bug.
// Nothing in the schema catches it — action has no CHECK coupling state to
// snooze_until the way project does — so it would have been silent.
func TestASnoozedActionIsNotWokenByTheGraph(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	blocker := addAction(t, st, "do this first", "write")
	blocked := addAction(t, st, "deferred on purpose", "run")

	// Snooze it, leaving the caller holding the pre-snooze image — which is
	// exactly what a caller that loaded early has.
	stale := blocked.Clone()
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := blocked.Clone()
	after.State = ActionSnoozed
	after.SnoozeUntil = sql.NullString{String: "2099-01-01", Valid: true}
	if _, err := tx.Update(ctx, blocked, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	blocking, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := blocking.AddBlocker(ctx, blocker, stale); err != nil {
		t.Fatalf("AddBlocker() returned error: %v", err)
	}
	if err := blocking.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	read, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer read.Rollback()
	got, err := read.LoadAction(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if got.State != ActionSnoozed {
		t.Errorf("%s is %s, want the snooze left alone", got.ID, got.State)
	}
}
