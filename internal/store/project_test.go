package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// loggedEvent is the subset of an event row the tests assert on.
type loggedEvent struct {
	Seq         int64
	Correlation string
	Actor       string
	Kind        string
	Severity    string
	SubjectType string
	SubjectID   string
	Field       string
	Old         string
	New         string
}

// newStore returns a Store over a fresh initialised database, with time
// pinned so timestamps are predictable.
func newStore(t *testing.T) *Store {
	t.Helper()
	path := newDBPath(t)
	if _, _, err := Init(path, testPrefixes()); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := New(db)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	// Advance a second per transaction, so timestamps are predictable but
	// two units of work are still distinguishable.
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var ticks int
	st.now = func() time.Time {
		ticks++
		return base.Add(time.Duration(ticks) * time.Second)
	}
	return st
}

func events(t *testing.T, st *Store) []loggedEvent {
	t.Helper()
	rows, err := st.db.Query(`SELECT seq, correlation, actor, kind, severity,
	                                 subject_type, subject_id, field, old_value, new_value
	                          FROM event ORDER BY seq`)
	if err != nil {
		t.Fatalf("querying events: %v", err)
	}
	defer rows.Close()

	var got []loggedEvent
	for rows.Next() {
		var e loggedEvent
		err := rows.Scan(&e.Seq, &e.Correlation, &e.Actor, &e.Kind, &e.Severity,
			&e.SubjectType, &e.SubjectID, &e.Field, &e.Old, &e.New)
		if err != nil {
			t.Fatalf("scanning event: %v", err)
		}
		got = append(got, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading events: %v", err)
	}
	return got
}

// insertProject creates and commits one project, returning it.
func insertProject(t *testing.T, st *Store, title string) *Project {
	t.Helper()
	ctx := context.Background()

	p, err := st.NewProject(ctx, title)
	if err != nil {
		t.Fatalf("NewProject() returned error: %v", err)
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
	return p
}

func TestAllocateIsSequentialAndPrefixed(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for i, want := range []string{"SL1", "SL2", "SL3"} {
		got, err := st.Allocate(ctx, EntityProject)
		if err != nil {
			t.Fatalf("Allocate() %d returned error: %v", i, err)
		}
		if got.ID != want {
			t.Errorf("Allocate() %d = %q, want %q", i, got.ID, want)
		}
		if got.Kind != "SL" || got.N != int64(i+1) {
			t.Errorf("Allocate() %d = %+v, want kind SL and n %d", i, got, i+1)
		}
	}
}

func TestAllocateIndependentPerEntity(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	project, err := st.Allocate(ctx, EntityProject)
	if err != nil {
		t.Fatalf("Allocate(project) returned error: %v", err)
	}
	action, err := st.Allocate(ctx, EntityAction)
	if err != nil {
		t.Fatalf("Allocate(action) returned error: %v", err)
	}
	if project.ID != "SL1" || action.ID != "NA1" {
		t.Errorf("got %q and %q, want SL1 and NA1", project.ID, action.ID)
	}
}

// TestAllocateSurvivesRolledBackInsert is the reason Allocate does not take a
// Tx: a failed create must still consume the number, or an identifier already
// cited in conversation could be handed out twice.
func TestAllocateSurvivesRolledBackInsert(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	p, err := st.NewProject(ctx, "doomed")
	if err != nil {
		t.Fatalf("NewProject() returned error: %v", err)
	}
	if p.ID != "SL1" {
		t.Fatalf("first project id = %q, want SL1", p.ID)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Insert(ctx, p); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() returned error: %v", err)
	}

	next, err := st.NewProject(ctx, "real")
	if err != nil {
		t.Fatalf("NewProject() returned error: %v", err)
	}
	if want := "SL2"; next.ID != want {
		t.Errorf("after a rolled back insert, next id = %q, want %q — the number must stay consumed", next.ID, want)
	}
}

func TestInsertLogsCreated(t *testing.T) {
	st := newStore(t)
	p := insertProject(t, st, "add a /healthz endpoint")

	got := events(t, st)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	want := loggedEvent{
		Seq: 1, Correlation: got[0].Correlation, Actor: "human", Kind: "created",
		Severity: "info", SubjectType: "project", SubjectID: p.ID,
	}
	if got[0] != want {
		t.Errorf("event = %+v, want %+v", got[0], want)
	}
	if got[0].Correlation == "" {
		t.Error("event has no correlation id")
	}
}

func TestInsertRoundTrips(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "round trip")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	loaded, err := tx.LoadProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("LoadProject() returned error: %v", err)
	}
	if *loaded != *p {
		t.Errorf("loaded = %+v, want %+v", *loaded, *p)
	}

	// Insert is responsible for both timestamps; an empty one would satisfy
	// the NOT NULL constraint and go unnoticed.
	if loaded.CreatedAt == "" {
		t.Error("created_at is empty after Insert()")
	}
	if loaded.UpdatedAt == "" {
		t.Error("updated_at is empty after Insert()")
	}
	if _, err := time.Parse(timeFormat, loaded.CreatedAt); err != nil {
		t.Errorf("created_at %q does not parse as %s: %v", loaded.CreatedAt, timeFormat, err)
	}
}

// TestCreatedAtIsImmutable pins the other half of the created tag: the store
// stamps it, and nothing may change it afterwards.
func TestCreatedAtIsImmutable(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "subject")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.CreatedAt = "2020-01-01T00:00:00.000Z"

	if _, err := tx.Update(ctx, p, after); err == nil {
		t.Error("Update() rewrote created_at, want an error")
	}
}

func TestLoadProjectMissing(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.LoadProject(ctx, "SL404"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("LoadProject() error = %v, want sql.ErrNoRows", err)
	}
}

func TestUpdateLogsOneEventPerField(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "before")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.Title = "after"
	after.Priority = sql.NullInt64{Int64: 2, Valid: true}

	changes, err := tx.Update(ctx, p, after)
	if err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("Update() returned %d changes, want 2", len(changes))
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	got := events(t, st)
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3 (one created, two changed)", len(got))
	}

	changed := got[1:]
	if changed[0].Correlation != changed[1].Correlation {
		t.Error("the two field changes have different correlation ids, want one per command")
	}
	if changed[0].Correlation == got[0].Correlation {
		t.Error("the update shares a correlation id with the insert, want one per command")
	}

	want := []loggedEvent{
		{Kind: "changed", Field: "title", Old: "before", New: "after"},
		{Kind: "changed", Field: "priority", Old: "", New: "2"},
	}
	for i, w := range want {
		if changed[i].Kind != w.Kind || changed[i].Field != w.Field ||
			changed[i].Old != w.Old || changed[i].New != w.New {
			t.Errorf("event %d = {%s %s %q→%q}, want {%s %s %q→%q}",
				i, changed[i].Kind, changed[i].Field, changed[i].Old, changed[i].New,
				w.Kind, w.Field, w.Old, w.New)
		}
	}
}

func TestUpdatePersists(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "before")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := p.Clone()
	after.Title = "after"
	if _, err := tx.Update(ctx, p, after); err != nil {
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

	loaded, err := read.LoadProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("LoadProject() returned error: %v", err)
	}
	if loaded.Title != "after" {
		t.Errorf("title = %q, want %q", loaded.Title, "after")
	}
	if loaded.UpdatedAt == p.UpdatedAt {
		t.Error("updated_at was not advanced by the update")
	}
	// The caller's struct must agree with what was written.
	if loaded.UpdatedAt != after.UpdatedAt {
		t.Errorf("stored updated_at = %q, but the caller's struct holds %q",
			loaded.UpdatedAt, after.UpdatedAt)
	}
	if loaded.CreatedAt != p.CreatedAt {
		t.Errorf("created_at changed from %q to %q", p.CreatedAt, loaded.CreatedAt)
	}
}

// TestUpdateNoOp checks that an update with nothing to say writes nothing at
// all — no row touched, no event, no advanced updated_at.
func TestUpdateNoOp(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "unchanged")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	changes, err := tx.Update(ctx, p, p.Clone())
	if err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("Update() returned %d changes, want 0", len(changes))
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if got := events(t, st); len(got) != 1 {
		t.Errorf("got %d events, want only the original created event", len(got))
	}
}

// TestActorPermissions is the schema's founding rule: sync may only write
// observed fields, and a human may only write authored ones.
func TestActorPermissions(t *testing.T) {
	tests := []struct {
		name    string
		actor   Actor
		mutate  func(*Project)
		wantErr bool
	}{
		{
			name:   "human writes an authored field",
			actor:  ActorHuman,
			mutate: func(p *Project) { p.Title = "new" },
		},
		{
			name:    "human writes an observed field",
			actor:   ActorHuman,
			mutate:  func(p *Project) { p.JiraStatus = sql.NullString{String: "Done", Valid: true} },
			wantErr: true,
		},
		{
			name:   "sync writes an observed field",
			actor:  ActorSyncJira,
			mutate: func(p *Project) { p.JiraStatus = sql.NullString{String: "Done", Valid: true} },
		},
		{
			name:    "sync writes an authored field",
			actor:   ActorSyncJira,
			mutate:  func(p *Project) { p.Title = "sync should not decide this" },
			wantErr: true,
		},
		{
			name:   "agent writes an authored field",
			actor:  ActorAgentClaude,
			mutate: func(p *Project) { p.Summary = "written by an agent" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStore(t)
			ctx := context.Background()
			p := insertProject(t, st, "subject")

			tx, err := st.Begin(ctx, tt.actor)
			if err != nil {
				t.Fatalf("Begin() returned error: %v", err)
			}
			defer tx.Rollback()

			after := p.Clone()
			tt.mutate(after)

			_, err = tx.Update(ctx, p, after)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("Update() error = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

// TestSyncCannotOverwriteAJudgement is the concrete failure the rule exists
// to prevent, kept as its own test because it is the point of the design.
func TestSyncCannotOverwriteAJudgement(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "subject")

	tx, err := st.Begin(ctx, ActorSyncJira)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.Status = ProjectDone // an authored judgement

	_, err = tx.Update(ctx, p, after)
	if err == nil {
		t.Fatal("sync wrote an authored field, want an error")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Errorf("error = %v, want it to name the status column", err)
	}
}

func TestNoteAndException(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "subject")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Note(ctx, p, "worth remembering"); err != nil {
		t.Fatalf("Note() returned error: %v", err)
	}
	if err := tx.Exception(ctx, p, "unexpected_review_state", "needs a look"); err != nil {
		t.Fatalf("Exception() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	got := events(t, st)
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[1].Kind != "note" || got[1].Severity != SeverityInfo {
		t.Errorf("note event = {%s %s}, want {note info}", got[1].Kind, got[1].Severity)
	}
	if got[2].Kind != "unexpected_review_state" || got[2].Severity != SeverityException {
		t.Errorf("exception event = {%s %s}, want {unexpected_review_state exception}",
			got[2].Kind, got[2].Severity)
	}
}

// TestRollbackDiscardsEvents checks the log is written in the same
// transaction as the change, so a failed command leaves no trace of a change
// that did not happen.
func TestRollbackDiscardsEvents(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "subject")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := p.Clone()
	after.Title = "rolled back"
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() returned error: %v", err)
	}

	if got := events(t, st); len(got) != 1 {
		t.Errorf("got %d events after rollback, want only the original created event", len(got))
	}
}
