package ghsync

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// issueFetcher answers about issues as well as pull requests.
type issueFetcher struct {
	*fakeFetcher
	issues  []github.Issue
	missing map[string]string
	asked   []string
}

func (f *issueFetcher) Issues(_ context.Context, keys []string) (github.IssueResult, error) {
	f.asked = append(f.asked, keys...)
	missing := f.missing
	if missing == nil {
		missing = map[string]string{}
	}
	return github.IssueResult{Issues: f.issues, Missing: missing}, nil
}

// trackIssue creates a project tracking a GitHub issue, and returns both ids.
func trackIssue(t *testing.T, st *store.Store, key string) (project string) {
	t.Helper()
	ctx := context.Background()

	p := store.NewProject("the work")
	if err := st.AllocateProject(ctx, p); err != nil {
		t.Fatalf("AllocateProject() returned error: %v", err)
	}
	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	if err := tx.Insert(ctx, p); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.LinkProjectIssue(ctx, p.ID, store.TrackerGitHub, key); err != nil {
		t.Fatalf("LinkProjectIssue() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return p.ID
}

func openAction(t *testing.T, st *store.Store, project, title string) *store.Action {
	t.Helper()
	ctx := context.Background()

	a := store.NewAction(title, "write")
	a.ProjectID = sql.NullString{String: project, Valid: true}
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}
	tx, err := st.Begin(ctx, store.ActorHuman)
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

func exceptionsOfKind(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	events, err := st.Events(context.Background(),
		store.EventQuery{Severity: store.SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// TestIssueStateIsRead is the half that was missing: the schema has carried a
// tracker discriminator since 0016, and nothing ever asked GitHub anything.
func TestIssueStateIsRead(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	trackIssue(t, st, "owner/repo#7")

	client := &issueFetcher{fakeFetcher: &fakeFetcher{}, issues: []github.Issue{{
		Key: "owner/repo#7", Title: "Make it work", State: "OPEN",
		Milestone: "v2", Assignees: []string{"alice"},
	}}}

	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.Issues) != 1 {
		t.Fatalf("Sync() reported %d issues, want 1", len(result.Issues))
	}

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	issue, err := tx.LoadTrackerIssue(ctx, store.IssueID(store.TrackerGitHub, "owner/repo#7"))
	if err != nil {
		t.Fatalf("LoadTrackerIssue() returned error: %v", err)
	}
	if issue.Summary != "Make it work" {
		t.Errorf("summary = %q", issue.Summary)
	}
	// GitHub's own word, not one of ours.
	if issue.Status.String != "OPEN" {
		t.Errorf("status = %q, want GitHub's own vocabulary", issue.Status.String)
	}
	if issue.Iteration.String != "v2" {
		t.Errorf("iteration = %q, want the milestone", issue.Iteration.String)
	}
	if issue.Assignee.String != "alice" {
		t.Errorf("assignee = %q", issue.Assignee.String)
	}
	if !issue.SyncedAt.Valid {
		t.Error("synced_at was not recorded")
	}
}

// TestAnIssueClosingWithWorkOpenIsAnException: somebody closed the ticket and
// the queue still thinks there is something to do, which means either the
// queue is stale or the ticket went early. Both are worth a look.
func TestAnIssueClosingWithWorkOpenIsAnException(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	project := trackIssue(t, st, "owner/repo#7")
	action := openAction(t, st, project, "finish it")

	client := &issueFetcher{fakeFetcher: &fakeFetcher{}, issues: []github.Issue{
		{Key: "owner/repo#7", Title: "Make it work", State: "OPEN"},
	}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if got := exceptionsOfKind(t, st, eventIssueClosedWithWork); got != 0 {
		t.Fatalf("an open issue raised %d exceptions", got)
	}

	// It closes.
	client.issues[0].State = "CLOSED"
	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.IssuesClosedWithWork) != 1 {
		t.Fatalf("Sync() reported %v, want the closure", result.IssuesClosedWithWork)
	}
	// The message names what is still open, since that is what to look at.
	if !strings.Contains(result.IssuesClosedWithWork[0], action.ID) {
		t.Errorf("the report does not name %s: %q", action.ID, result.IssuesClosedWithWork[0])
	}
	if got := exceptionsOfKind(t, st, eventIssueClosedWithWork); got != 1 {
		t.Errorf("logged %d exceptions, want 1", got)
	}

	// Still closed, still open work: a standing condition, not news again.
	for i := 0; i < 3; i++ {
		if _, err := Sync(ctx, st, client); err != nil {
			t.Fatalf("Sync() returned error: %v", err)
		}
	}
	if got := exceptionsOfKind(t, st, eventIssueClosedWithWork); got != 1 {
		t.Errorf("logged %d exceptions over four polls, want 1", got)
	}
}

// TestAnIssueClosingWithNothingOpenIsQuiet, which is the ordinary happy case
// and worth nothing at all.
func TestAnIssueClosingWithNothingOpenIsQuiet(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	trackIssue(t, st, "owner/repo#7")

	client := &issueFetcher{fakeFetcher: &fakeFetcher{}, issues: []github.Issue{
		{Key: "owner/repo#7", Title: "Make it work", State: "OPEN"},
	}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	client.issues[0].State = "CLOSED"
	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.IssuesClosedWithWork) != 0 {
		t.Errorf("reported %v for an issue with no open work", result.IssuesClosedWithWork)
	}
	if got := exceptionsOfKind(t, st, eventIssueClosedWithWork); got != 0 {
		t.Errorf("logged %d exceptions, want none", got)
	}
}

// TestAnIssueAlreadyClosedIsNotNews: the exception is about the transition. An
// issue that was closed before roz ever saw it never closed as far as roz is
// concerned.
func TestAnIssueAlreadyClosedIsNotNews(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	project := trackIssue(t, st, "owner/repo#7")
	openAction(t, st, project, "finish it")

	client := &issueFetcher{fakeFetcher: &fakeFetcher{}, issues: []github.Issue{
		{Key: "owner/repo#7", Title: "Make it work", State: "CLOSED"},
	}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if got := exceptionsOfKind(t, st, eventIssueClosedWithWork); got != 0 {
		t.Errorf("an issue closed before roz saw it raised %d exceptions", got)
	}
}

// TestAnUnreadableIssueIsReported: the commonest cause is a key naming a pull
// request, and the symptom without this is that nothing ever updates.
func TestAnUnreadableIssueIsReported(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	trackIssue(t, st, "owner/repo#7")

	client := &issueFetcher{
		fakeFetcher: &fakeFetcher{},
		missing:     map[string]string{"owner/repo#7": "is not an issue GitHub will show us"},
	}
	for i := 0; i < 3; i++ {
		if _, err := Sync(ctx, st, client); err != nil {
			t.Fatalf("Sync() returned error: %v", err)
		}
	}
	if got := exceptionsOfKind(t, st, eventIssueUnreadable); got != 1 {
		t.Errorf("logged %d exceptions over three polls, want 1", got)
	}
}

// TestIssuesAreReadWithNoPullRequestsTracked: a project can track an issue in
// a database that tracks no pull requests at all.
func TestIssuesAreReadWithNoPullRequestsTracked(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	trackIssue(t, st, "owner/repo#7")

	client := &issueFetcher{fakeFetcher: &fakeFetcher{}, issues: []github.Issue{
		{Key: "owner/repo#7", Title: "Make it work", State: "OPEN"},
	}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(client.asked) != 1 || client.asked[0] != "owner/repo#7" {
		t.Errorf("asked about %v, want the tracked issue", client.asked)
	}
}

// TestReadingAnIssueAgainIsNotNews: a syncer left running reads every tracked
// issue on every poll, and synced_at moves each time.
func TestReadingAnIssueAgainIsNotNews(t *testing.T) {
	ctx := context.Background()
	st := newBareStore(t)
	trackIssue(t, st, "owner/repo#7")

	client := &issueFetcher{fakeFetcher: &fakeFetcher{}, issues: []github.Issue{
		{Key: "owner/repo#7", Title: "Make it work", State: "OPEN"},
	}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.Issues) != 0 {
		t.Errorf("Sync() reported %v for an unchanged issue", result.Issues[0].Changes)
	}
	// Still polled, and still recorded: only the report is quiet.
	if result.IssuesPolled != 1 {
		t.Errorf("IssuesPolled = %d, want 1", result.IssuesPolled)
	}
	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	issue, err := tx.LoadTrackerIssue(ctx, store.IssueID(store.TrackerGitHub, "owner/repo#7"))
	if err != nil {
		t.Fatalf("LoadTrackerIssue() returned error: %v", err)
	}
	if !issue.SyncedAt.Valid {
		t.Error("synced_at was not written")
	}
}
