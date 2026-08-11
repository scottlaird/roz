package store

import (
	"context"
	"strings"
	"testing"
)

func verify(t *testing.T, st *Store, r Record, at string) []Change {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorVerify)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	changes, err := tx.Verify(ctx, r, at)
	if err != nil {
		t.Fatalf("Verify() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return changes
}

func TestVerifyStampsProjectsAndActions(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	p := insertProject(t, st, "Split the nodepool")
	a := addAction(t, st, "write the endpoint", "write")

	verify(t, st, p, "2026-08-04T00:00:00.000Z")
	verify(t, st, a, "2026-08-04T00:00:00.000Z")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	loadedProject, err := tx.LoadProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("LoadProject() returned error: %v", err)
	}
	if got := loadedProject.LastVerifiedAt.String; got != "2026-08-04T00:00:00.000Z" {
		t.Errorf("project last_verified_at = %q", got)
	}
	loadedAction, err := tx.LoadAction(ctx, a.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if got := loadedAction.LastVerifiedAt.String; got != "2026-08-04T00:00:00.000Z" {
		t.Errorf("action last_verified_at = %q", got)
	}
}

// TestVerifyChangesNothingElse is the command's own promise, and the reason it
// is safe to run on anything at any time.
func TestVerifyChangesNothingElse(t *testing.T) {
	st := newStore(t)

	p := insertProject(t, st, "Split the nodepool")
	changes := verify(t, st, p, "2026-08-04T00:00:00.000Z")

	if len(changes) != 1 || changes[0].Column != "last_verified_at" {
		t.Errorf("Verify() changed %v, want only last_verified_at", changes)
	}
}

// TestVerifyIsIdempotent: told the same thing twice, it writes nothing. The
// diff decides, so there is no second event and no second timestamp.
func TestVerifyIsIdempotent(t *testing.T) {
	st := newStore(t)

	p := insertProject(t, st, "Split the nodepool")
	verify(t, st, p, "2026-08-04T00:00:00.000Z")

	tx, err := st.Begin(context.Background(), ActorVerify)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	reloaded, err := tx.LoadProject(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("LoadProject() returned error: %v", err)
	}
	changes, err := tx.Verify(context.Background(), reloaded, "2026-08-04T00:00:00.000Z")
	if err != nil {
		t.Fatalf("Verify() returned error: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("re-verifying wrote %v, want nothing", changes)
	}
}

// TestVerifyNeedsItsOwnActor: last_verified_at is observed, so a person
// running this through the ordinary path is exactly what the founding rule
// refuses. The command supplies the actor; nobody else may.
func TestVerifyNeedsItsOwnActor(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	p := insertProject(t, st, "Split the nodepool")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Verify(ctx, p, "2026-08-04T00:00:00.000Z"); err == nil {
		t.Error("a human verified directly, want the observed rule to refuse it")
	}
}

// TestVerifyRefusesWhatCannotGoStale: sync reads a pull request every minute,
// so "when did anyone last check" is never the question about one.
func TestVerifyRefusesWhatCannotGoStale(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/todo")
	pr := trackPR(t, st, "scottlaird/todo", 1)

	tx, err := st.Begin(ctx, ActorVerify)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	_, err = tx.Verify(ctx, pr, "2026-08-04T00:00:00.000Z")
	if err == nil {
		t.Fatal("Verify() accepted a pull request")
	}
	if !strings.Contains(err.Error(), "cannot be verified") {
		t.Errorf("error = %v", err)
	}
}

// TestStalenessOrdersLeastRecentlyCheckedFirst, with never-checked ahead of
// everything — the one place unstated sorts first rather than last, because a
// missing verification is the information rather than the absence of it.
func TestStalenessOrdersLeastRecentlyCheckedFirst(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Created newest-checked first, so creation order cannot produce a pass.
	recent := insertProject(t, st, "checked yesterday")
	old := insertProject(t, st, "checked last week")
	never := insertProject(t, st, "never checked")
	verify(t, st, recent, "2026-08-10T00:00:00.000Z")
	verify(t, st, old, "2026-08-04T00:00:00.000Z")

	found, err := st.ListProjects(ctx, ProjectFilter{Order: OrderStaleness})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	want := []string{never.ID, old.ID, recent.ID}
	if got := projectIDs(found); !equalStrings(got, want) {
		t.Errorf("staleness order = %v, want %v", got, want)
	}
}

func TestStalenessOrdersActionsToo(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	recent := addAction(t, st, "checked yesterday", "write")
	old := addAction(t, st, "checked last week", "write")
	never := addAction(t, st, "never checked", "write")
	verify(t, st, recent, "2026-08-10T00:00:00.000Z")
	verify(t, st, old, "2026-08-04T00:00:00.000Z")

	found, err := st.ListActions(ctx, ActionFilter{Order: OrderStaleness})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	want := []string{never.ID, old.ID, recent.ID}
	if got := ids(found); !equalStrings(got, want) {
		t.Errorf("staleness order = %v, want %v", got, want)
	}
}

// TestStalenessStillFilters: the ordering must not change which rows come
// back, only their order.
func TestStalenessStillFilters(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	ready := addAction(t, st, "ready work", "write")
	blocked := addAction(t, st, "blocked work", "run")
	blockOn(t, st, blocked, ready)

	found, err := st.ListActions(ctx, ActionFilter{Unblocked: true, Order: OrderStaleness})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if !equalStrings(ids(found), []string{ready.ID}) {
		t.Errorf("--unblocked --sort staleness returned %v, want [%s]", ids(found), ready.ID)
	}
}
