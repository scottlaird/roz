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
		if err := tx.LinkProjectIssue(ctx, p.ID, TrackerJira, key); err != nil {
			t.Fatalf("LinkProjectIssue() returned error: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return p
}

// observeJira fills in the tracker, since every observation in this file is a
// Jira one and repeating it would bury what each case is actually about.
func observeJira(t *testing.T, st *Store, observations ...TrackerObservation) *TrackerResult {
	t.Helper()

	for i := range observations {
		if observations[i].Tracker == "" {
			observations[i].Tracker = TrackerJira
		}
	}
	result, err := st.ObserveTrackerIssues(context.Background(), ActorJiraManual, observations)
	if err != nil {
		t.Fatalf("ObserveTrackerIssues() returned error: %v", err)
	}
	return result
}

// loadIssue takes the tracker's own key, and composes the id.
func loadIssue(t *testing.T, st *Store, key string) *TrackerIssue {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	issue, err := tx.LoadTrackerIssue(ctx, IssueID(TrackerJira, key))
	if err != nil {
		t.Fatalf("LoadTrackerIssue(%s) returned error: %v", key, err)
	}
	return issue
}

func TestObserveTrackerIssues(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, TrackerObservation{
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

	keys, err := tx.IssueIDsForProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("IssueIDsForProject() returned error: %v", err)
	}
	if !equalStrings(keys, []string{"jira:CDSS-1392", "jira:CDSS-1393"}) {
		t.Errorf("IssueIDsForProject() = %v, want both issues", keys)
	}
}

// TestUnreportedFieldsAreLeftAlone: absence is not a fact.
func TestUnreportedFieldsAreLeftAlone(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, TrackerObservation{
		Key:       "CDSS-1744",
		Iteration: sql.NullString{String: "Sprint 42", Valid: true},
	})
	observeJira(t, st, TrackerObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	})

	issue := loadIssue(t, st, "CDSS-1744")
	if issue.Iteration.String != "Sprint 42" {
		t.Errorf("sprint = %q, want it left alone by an observation that did not mention it",
			issue.Iteration.String)
	}
}

// TestAnEmptyValueIsAFact: unassigning an issue is something Jira said.
func TestAnEmptyValueIsAFact(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, TrackerObservation{
		Key:      "CDSS-1744",
		Assignee: sql.NullString{String: "scott", Valid: true},
	})
	observeJira(t, st, TrackerObservation{
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

	observeJira(t, st, TrackerObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "To Do", Valid: true},
	})
	first := loadIssue(t, st, "CDSS-1744").SyncedAt.String

	observeJira(t, st, TrackerObservation{
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

	result := observeJira(t, st, TrackerObservation{
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

// TestObserveTrackerIssuesReportsEveryProjectSharingAKey: two projects watching one
// epic is reasonable, and both should be named.
func TestObserveTrackerIssuesReportsEveryProjectSharingAKey(t *testing.T) {
	st := newStore(t)
	first := linkJira(t, st, "one half", "CDSS-1744")
	second := linkJira(t, st, "the other half", "CDSS-1744")

	result := observeJira(t, st, TrackerObservation{
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
	if err := tx.UnlinkProjectIssue(ctx, p.ID, TrackerJira, "CDSS-1744"); err != nil {
		t.Fatalf("UnlinkProjectIssue() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if loadIssue(t, st, "CDSS-1744") == nil {
		t.Error("the issue went away with the link")
	}
}

// TestHumansMayNotObserveTrackerIssues: the whole reason for a separate actor is that
// observed columns are not a person's to write.
func TestHumansMayNotObserveTrackerIssues(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	_, err := st.ObserveTrackerIssues(context.Background(), ActorHuman, []TrackerObservation{{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	}})
	if err == nil {
		t.Fatal("ObserveTrackerIssues() as a human was accepted")
	}
	if !strings.Contains(err.Error(), "observed") {
		t.Errorf("error = %v, want it to say the fields are observed", err)
	}
}

// TestTrackerObservationsAreLoggedAsManual: the log must not claim Jira said
// something that was typed in by hand.
func TestTrackerObservationsAreLoggedAsManual(t *testing.T) {
	st := newStore(t)
	linkJira(t, st, "Split the nodepool", "CDSS-1744")

	observeJira(t, st, TrackerObservation{
		Key:    "CDSS-1744",
		Status: sql.NullString{String: "Done", Valid: true},
	})

	logged := events(t, st)
	var found bool
	for _, e := range logged {
		if e.SubjectID == IssueID(TrackerJira, "CDSS-1744") && e.Field == "status" {
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

// TestIssueKeysAreScopedToTheirTracker: a sync asks about the issues of one
// tracker, and a Jira key means nothing to GitHub.
func TestIssueKeysAreScopedToTheirTracker(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	p := insertProject(t, st, "the work")
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	for _, link := range []struct{ tracker, key string }{
		{TrackerGitHub, "owner/repo#7"},
		{TrackerGitHub, "owner/repo#3"},
		{TrackerJira, "CDSS-1744"},
	} {
		if err := tx.LinkProjectIssue(ctx, p.ID, link.tracker, link.key); err != nil {
			t.Fatalf("LinkProjectIssue() returned error: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	got, err := st.IssueKeys(ctx, TrackerGitHub)
	if err != nil {
		t.Fatalf("IssueKeys() returned error: %v", err)
	}
	want := []string{"owner/repo#3", "owner/repo#7"}
	if len(got) != len(want) {
		t.Fatalf("IssueKeys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("IssueKeys()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestOpenActionsForIssueIsTheUnionOverProjects: an issue may be tracked by
// more than one project, and nothing records which action belongs to which
// issue, so the answer is every open action on every project that tracks it.
func TestOpenActionsForIssueIsTheUnionOverProjects(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	first := insertProject(t, st, "the work")
	second := insertProject(t, st, "the other half")
	other := insertProject(t, st, "something else")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	for _, p := range []*Project{first, second} {
		if err := tx.LinkProjectIssue(ctx, p.ID, TrackerGitHub, "owner/repo#7"); err != nil {
			t.Fatalf("LinkProjectIssue() returned error: %v", err)
		}
	}
	if err := tx.LinkProjectIssue(ctx, other.ID, TrackerGitHub, "owner/repo#8"); err != nil {
		t.Fatalf("LinkProjectIssue() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	open := actionOn(t, st, first, "finish it")
	alsoOpen := actionOn(t, st, second, "and this")
	actionOn(t, st, other, "not this")
	closed := actionOn(t, st, first, "already done")
	closeAction(t, st, closed)

	got, err := st.OpenActionsForIssue(ctx, IssueID(TrackerGitHub, "owner/repo#7"))
	if err != nil {
		t.Fatalf("OpenActionsForIssue() returned error: %v", err)
	}
	want := []string{open.ID, alsoOpen.ID}
	if len(got) != len(want) {
		t.Fatalf("OpenActionsForIssue() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("OpenActionsForIssue()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// actionOn adds an open action to a project.
func actionOn(t *testing.T, st *Store, p *Project, title string) *Action {
	t.Helper()
	ctx := context.Background()

	a := NewAction(title, "write")
	a.ProjectID = sql.NullString{String: p.ID, Valid: true}
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

func closeAction(t *testing.T, st *Store, a *Action) {
	t.Helper()

	if _, err := st.CloseAction(context.Background(), ActorHuman,
		CloseRequest{ID: a.ID}); err != nil {
		t.Fatalf("CloseAction() returned error: %v", err)
	}
}
