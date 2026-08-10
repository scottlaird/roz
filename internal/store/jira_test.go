package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// withJiraKey gives a project a Jira issue key.
func withJiraKey(t *testing.T, st *Store, title, key string) *Project {
	t.Helper()
	ctx := context.Background()

	p := insertProject(t, st, title)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.JiraKey = sql.NullString{String: key, Valid: true}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	*p = *after
	return p
}

func observeJira(t *testing.T, st *Store, observations ...JiraObservation) *JiraResult {
	t.Helper()

	result, err := st.ObserveJira(context.Background(), ActorJiraManual, observations)
	if err != nil {
		t.Fatalf("ObserveJira() returned error: %v", err)
	}
	return result
}

func TestObserveJira(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := withJiraKey(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:      "CDSS-1744",
		Status:   sql.NullString{String: "In Progress", Valid: true},
		Assignee: sql.NullString{String: "scott", Valid: true},
	})

	loaded := reload(t, st, p.ID)
	if loaded.JiraStatus.String != "In Progress" || loaded.JiraAssignee.String != "scott" {
		t.Errorf("project = %+v, want the observation applied", loaded)
	}
	if !loaded.JiraSyncedAt.Valid {
		t.Error("jira_synced_at was not stamped")
	}
	_ = ctx
}

func reload(t *testing.T, st *Store, id string) *Project {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	p, err := tx.LoadProject(ctx, id)
	if err != nil {
		t.Fatalf("LoadProject(%s) returned error: %v", id, err)
	}
	return p
}

// TestUnreportedFieldsAreLeftAlone is the sketch's rule: absence is not a
// fact. A sync that says nothing about the sprint has not emptied it.
func TestUnreportedFieldsAreLeftAlone(t *testing.T) {
	st := newStore(t)
	p := withJiraKey(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "In Progress", Valid: true},
		Sprint: sql.NullString{String: "Sprint 41", Valid: true},
	})
	observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	})

	loaded := reload(t, st, p.ID)
	if loaded.JiraSprint.String != "Sprint 41" {
		t.Errorf("jira_sprint = %q, want it left alone by a report that omitted it",
			loaded.JiraSprint.String)
	}
}

// TestAnEmptyValueIsAFact is the other side of it: unassigning an issue is
// something Jira says, not something it fails to say.
func TestAnEmptyValueIsAFact(t *testing.T) {
	st := newStore(t)
	p := withJiraKey(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:      "CDSS-1744",
		Assignee: sql.NullString{String: "scott", Valid: true},
	})
	observeJira(t, st, JiraObservation{
		Key:      "CDSS-1744",
		Assignee: sql.NullString{String: "", Valid: true},
	})

	if got := reload(t, st, p.ID).JiraAssignee.String; got != "" {
		t.Errorf("jira_assignee = %q, want it cleared by an explicit empty value", got)
	}
}

// TestSyncedAtMovesWithoutOtherChanges: "nothing has changed since Tuesday"
// and "nobody has looked since Tuesday" are different things.
func TestSyncedAtMovesWithoutOtherChanges(t *testing.T) {
	st := newStore(t)
	p := withJiraKey(t, st, "Split the nodepool", "CDSS-1744")

	observation := JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "In Progress", Valid: true},
	}
	observeJira(t, st, observation)
	first := reload(t, st, p.ID).JiraSyncedAt.String

	observeJira(t, st, observation)
	second := reload(t, st, p.ID).JiraSyncedAt.String

	if first == second {
		t.Errorf("jira_synced_at stayed at %s across two reads", first)
	}
}

func TestObserveJiraSpreadsAcrossProjectsSharingAKey(t *testing.T) {
	st := newStore(t)

	first := withJiraKey(t, st, "one half", "CDSS-1744")
	second := withJiraKey(t, st, "the other half", "CDSS-1744")

	result := observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "In Progress", Valid: true},
	})
	if len(result.Applied) != 2 {
		t.Fatalf("applied to %d projects, want both", len(result.Applied))
	}
	for _, p := range []*Project{first, second} {
		if got := reload(t, st, p.ID).JiraStatus.String; got != "In Progress" {
			t.Errorf("%s jira_status = %q, want the observation", p.ID, got)
		}
	}
}

// TestUnmatchedKeysAreReportedNotRefused: this tracks a subset of what Jira
// holds, so a key nobody claims is normal.
func TestUnmatchedKeysAreReportedNotRefused(t *testing.T) {
	st := newStore(t)
	withJiraKey(t, st, "Split the nodepool", "CDSS-1744")

	result := observeJira(t, st,
		JiraObservation{Key: "CDSS-1744", Status: sql.NullString{String: "Done", Valid: true}},
		JiraObservation{Key: "CDSS-9999", Status: sql.NullString{String: "To Do", Valid: true}})

	if !equalStrings(result.Unmatched, []string{"CDSS-9999"}) {
		t.Errorf("Unmatched = %v, want [CDSS-9999]", result.Unmatched)
	}
	if len(result.Applied) != 1 {
		t.Errorf("Applied = %+v, want the one that matched", result.Applied)
	}
}

// TestHumansMayNotObserveJira: the whole reason for a separate actor is that
// these are observed columns, and a person writing them has to be doing it
// deliberately, as a sync.
func TestHumansMayNotObserveJira(t *testing.T) {
	st := newStore(t)
	withJiraKey(t, st, "Split the nodepool", "CDSS-1744")

	_, err := st.ObserveJira(context.Background(), ActorHuman, []JiraObservation{{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	}})
	if err == nil || !strings.Contains(err.Error(), "observed fields") {
		t.Errorf("error = %v, want a human refused", err)
	}
}

// TestJiraObservationsAreLoggedAsManual: the log must not claim Jira said
// something a person typed.
func TestJiraObservationsAreLoggedAsManual(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	withJiraKey(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	})

	events, err := st.Events(ctx, EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}

	var found bool
	for _, e := range events {
		if e.Field == "jira_status" {
			found = true
			if e.Actor != string(ActorJiraManual) {
				t.Errorf("actor = %q, want %q", e.Actor, ActorJiraManual)
			}
		}
	}
	if !found {
		t.Error("no event for jira_status")
	}
}
