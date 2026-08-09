package store

import (
	"context"
	"database/sql"
	"testing"
)

// snoozeProject puts a project into snoozed status with a date. The schema
// couples the two, so they have to move together.
func snoozeProject(t *testing.T, st *Store, p *Project, until string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.Status = ProjectSnoozed
	after.SnoozeUntil = sql.NullString{String: until, Valid: true}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func projectIDs(projects []*Project) []string {
	ids := make([]string, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	return ids
}

func TestListProjectsOrdersByNumber(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Ten projects, so lexical and numeric ordering disagree: SL10 sorts
	// before SL2 as text. Ordering on n is why n exists.
	for i := 0; i < 10; i++ {
		insertProject(t, st, "project")
	}

	got, err := st.ListProjects(ctx, ProjectFilter{})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	want := []string{"SL1", "SL2", "SL3", "SL4", "SL5", "SL6", "SL7", "SL8", "SL9", "SL10"}
	if got := projectIDs(got); !equalStrings(got, want) {
		t.Errorf("ListProjects() = %v, want %v", got, want)
	}
}

func TestListProjectsEmpty(t *testing.T) {
	st := newStore(t)

	got, err := st.ListProjects(context.Background(), ProjectFilter{})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListProjects() on an empty database = %v, want none", projectIDs(got))
	}
}

func TestListProjectsByStatus(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	insertProject(t, st, "active one")
	snoozed := insertProject(t, st, "snoozed one")
	snoozeProject(t, st, snoozed, "2030-01-01")

	got, err := st.ListProjects(ctx, ProjectFilter{Status: ProjectSnoozed})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	if want := []string{snoozed.ID}; !equalStrings(projectIDs(got), want) {
		t.Errorf("ListProjects(status=snoozed) = %v, want %v", projectIDs(got), want)
	}
}

// TestListProjectsExpired covers the sketch's highest-value query: a snooze
// whose date has passed and which nothing else would surface.
func TestListProjectsExpired(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// newStore pins the clock to 2026-08-09.
	past := insertProject(t, st, "revisit on Thursday")
	snoozeProject(t, st, past, "2026-08-06")

	future := insertProject(t, st, "after oncall")
	snoozeProject(t, st, future, "2026-08-21")

	insertProject(t, st, "not snoozed at all")
	_ = future

	got, err := st.ListProjects(ctx, ProjectFilter{Expired: true})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	if want := []string{past.ID}; !equalStrings(projectIDs(got), want) {
		t.Errorf("ListProjects(expired) = %v, want %v", projectIDs(got), want)
	}
}

func TestListProjectsOrphaned(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	orphan := insertProject(t, st, "no action, no snooze")
	snoozed := insertProject(t, st, "snoozed, so someone is watching")
	snoozeProject(t, st, snoozed, "2030-01-01")

	got, err := st.ListProjects(ctx, ProjectFilter{Orphaned: true})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	if want := []string{orphan.ID}; !equalStrings(projectIDs(got), want) {
		t.Errorf("ListProjects(orphaned) = %v, want %v", projectIDs(got), want)
	}
}

func TestListProjectsRoundTripsAllColumns(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "subject")

	got, err := st.ListProjects(ctx, ProjectFilter{})
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d projects, want 1", len(got))
	}
	if *got[0] != *p {
		t.Errorf("ListProjects() = %+v, want %+v", *got[0], *p)
	}
}

// TestInsertRejectsObservedFromHuman closes the loop the CLI relies on: only
// sync may populate an observed column, at creation as well as on update.
func TestInsertRejectsObservedFromHuman(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	p := NewProject("subject")
	if err := st.AllocateProject(ctx, p); err != nil {
		t.Fatalf("AllocateProject() returned error: %v", err)
	}
	p.JiraStatus = sql.NullString{String: "Done", Valid: true}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, p); err == nil {
		t.Error("Insert() let a human set jira_status, want an error")
	}
}

// TestSyncCannotCreateAProject records a consequence worth knowing: a project
// has authored columns that cannot be left empty — title and status, the
// latter with a CHECK constraint — so sync can never create one. Deciding a
// project exists is a judgement, and sync does not make judgements. Sync
// updates the observed columns of projects a human created.
func TestSyncCannotCreateAProject(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	p := NewProject("subject")
	if err := st.AllocateProject(ctx, p); err != nil {
		t.Fatalf("AllocateProject() returned error: %v", err)
	}
	p.JiraStatus = sql.NullString{String: "Done", Valid: true}

	tx, err := st.Begin(ctx, ActorSyncJira)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, p); err == nil {
		t.Error("Insert() let sync create a project, want an error")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
