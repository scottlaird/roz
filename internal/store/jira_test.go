package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// linkJira gives a project a Jira issue, creating the issue if it is new.
func linkJira(t *testing.T, st *Store, title string, keys ...string) *Project {
	t.Helper()
	ctx := context.Background()

	p := insertProject(t, st, title)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	for _, key := range keys {
		if err := tx.LinkProjectJira(ctx, p.ID, key); err != nil {
			t.Fatalf("LinkProjectJira() returned error: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
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

func loadIssue(t *testing.T, st *Store, key string) *JiraIssue {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	issue, err := tx.LoadJiraIssue(ctx, key)
	if err != nil {
		t.Fatalf("LoadJiraIssue(%s) returned error: %v", key, err)
	}
	return issue
}

func TestObserveJira(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:      "CDSS-1744",
		Status:   sql.NullString{String: "In Progress", Valid: true},
		Assignee: sql.NullString{String: "scott", Valid: true},
	})

	issue := loadIssue(t, st, "CDSS-1744")
	if issue.Status.String != "In Progress" || issue.Assignee.String != "scott" {
		t.Errorf("issue = %+v, want the observation applied", issue)
	}
	if !issue.SyncedAt.Valid {
		t.Error("synced_at was not stamped")
	}
}

// TestAProjectMayTrackSeveralIssues is why this is an entity at all: one piece
// of work maps to "allow scaling up" and "allow scaling down", and the column
// this replaced could hold one of them.
func TestAProjectMayTrackSeveralIssues(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := linkJira(t, st, "Walker resizing", "CDSS-1392", "CDSS-1393")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	keys, err := tx.JiraKeysForProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("JiraKeysForProject() returned error: %v", err)
	}
	if !equalStrings(keys, []string{"CDSS-1392", "CDSS-1393"}) {
		t.Errorf("JiraKeysForProject() = %v, want both issues", keys)
	}
}

// TestUnreportedFieldsAreLeftAlone: absence is not a fact.
func TestUnreportedFieldsAreLeftAlone(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Sprint: sql.NullString{String: "Sprint 42", Valid: true},
	})
	observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	})

	issue := loadIssue(t, st, "CDSS-1744")
	if issue.Sprint.String != "Sprint 42" {
		t.Errorf("sprint = %q, want it left alone by an observation that did not mention it",
			issue.Sprint.String)
	}
}

// TestAnEmptyValueIsAFact: unassigning an issue is something Jira said.
func TestAnEmptyValueIsAFact(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:      "CDSS-1744",
		Assignee: sql.NullString{String: "scott", Valid: true},
	})
	observeJira(t, st, JiraObservation{
		Key:      "CDSS-1744",
		Assignee: sql.NullString{String: "", Valid: true},
	})

	if got := loadIssue(t, st, "CDSS-1744").Assignee.String; got != "" {
		t.Errorf("assignee = %q, want it cleared by an explicit empty value", got)
	}
}

// TestSyncedAtMovesWithoutOtherChanges: "nothing has changed since Tuesday"
// and "nobody has looked since Tuesday" are different questions.
func TestSyncedAtMovesWithoutOtherChanges(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "To Do", Valid: true},
	})
	first := loadIssue(t, st, "CDSS-1744").SyncedAt.String

	observeJira(t, st, JiraObservation{
		Key:      "CDSS-1744",
		Status:   sql.NullString{String: "To Do", Valid: true},
		SyncedAt: "2027-01-01T00:00:00.000Z",
	})

	if got := loadIssue(t, st, "CDSS-1744").SyncedAt.String; got == first {
		t.Error("synced_at did not move when nothing else changed")
	}
}

// TestAnIssueNothingTracksIsStillRecorded: an issue is a record in its own
// right, so an observation about one nobody has claimed is stored rather than
// discarded. It was reported as unmatched and dropped before.
func TestAnIssueNothingTracksIsStillRecorded(t *testing.T) {
	st := newStore(t)

	result := observeJira(t, st, JiraObservation{
		Key:    "CDSS-9999",
		Status: sql.NullString{String: "To Do", Valid: true},
	})

	if len(result.Applied) != 1 || !result.Applied[0].Created {
		t.Fatalf("Applied = %+v, want one newly created issue", result.Applied)
	}
	if len(result.Applied[0].Projects) != 0 {
		t.Errorf("Projects = %v, want none", result.Applied[0].Projects)
	}
	if got := loadIssue(t, st, "CDSS-9999").Status.String; got != "To Do" {
		t.Errorf("status = %q, want it stored even with nothing tracking it", got)
	}
}

// TestObserveJiraReportsEveryProjectSharingAKey: two projects watching one
// epic is reasonable, and both should be named.
func TestObserveJiraReportsEveryProjectSharingAKey(t *testing.T) {
	st := newStore(t)
	first := linkJira(t, st, "one half", "CDSS-1744")
	second := linkJira(t, st, "the other half", "CDSS-1744")

	result := observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "In Progress", Valid: true},
	})

	if len(result.Applied) != 1 {
		t.Fatalf("Applied = %+v, want one issue", result.Applied)
	}
	if !equalStrings(result.Applied[0].Projects, []string{first.ID, second.ID}) {
		t.Errorf("Projects = %v, want both %s and %s",
			result.Applied[0].Projects, first.ID, second.ID)
	}
}

// TestUnlinkKeepsTheIssue: what Jira said is not invalidated by nobody
// tracking it any more.
func TestUnlinkKeepsTheIssue(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := linkJira(t, st, "Split the nodepool", "CDSS-1744")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.UnlinkProjectJira(ctx, p.ID, "CDSS-1744"); err != nil {
		t.Fatalf("UnlinkProjectJira() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if loadIssue(t, st, "CDSS-1744") == nil {
		t.Error("the issue went away with the link")
	}
}

// TestHumansMayNotObserveJira: the whole reason for a separate actor is that
// observed columns are not a person's to write.
func TestHumansMayNotObserveJira(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	_, err := st.ObserveJira(context.Background(), ActorHuman, []JiraObservation{{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	}})
	if err == nil {
		t.Fatal("ObserveJira() as a human was accepted")
	}
	if !strings.Contains(err.Error(), "observed") {
		t.Errorf("error = %v, want it to say the fields are observed", err)
	}
}

// TestJiraObservationsAreLoggedAsManual: the log must not claim Jira said
// something that was typed in by hand.
func TestJiraObservationsAreLoggedAsManual(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, JiraObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	})

	logged := events(t, st)
	var found bool
	for _, e := range logged {
		if e.SubjectID == "CDSS-1744" && e.Field == "status" {
			found = true
			if e.Actor != string(ActorJiraManual) {
				t.Errorf("actor = %q, want %q", e.Actor, ActorJiraManual)
			}
		}
	}
	if !found {
		t.Error("no status event was written against the issue")
	}
}
