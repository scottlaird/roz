package ghsync

import (
	"context"
	"testing"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// TestSyncRecordsWhatAPullRequestCloses. The association existed only in the
// body until now: roz stored the prose and never read it, so "which issues did
// I move this week" had nothing to join on.
func TestSyncRecordsWhatAPullRequestCloses(t *testing.T) {
	st, key := newStore(t)
	pr := observed(key)
	pr.ClosingIssues = []string{"scottlaird/roz#214", "otherorg/other#5"}

	if _, err := Sync(context.Background(), st, &fakeFetcher{
		result: github.Result{PullRequests: []github.PullRequest{pr}},
	}); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	want := []string{"github:otherorg/other#5", "github:scottlaird/roz#214"}
	if got := issuesFor(t, st, key); !equal(got, want) {
		t.Errorf("issues = %v, want %v", got, want)
	}

	// The issue rows exist, which is what puts them in the poll: a link to an
	// issue nothing has recorded is a link to a row that never updates.
	polled, err := st.IssueKeys(context.Background(), store.TrackerGitHub)
	if err != nil {
		t.Fatalf("IssueKeys() returned error: %v", err)
	}
	if !equal(polled, []string{"otherorg/other#5", "scottlaird/roz#214"}) {
		t.Errorf("polled issues = %v, want both, including the one in another repository", polled)
	}
}

// TestSyncReconcilesWhatAPullRequestCloses is the decision on #167: a closing
// keyword edited out of a body takes its link with it. A link that could only
// ever be added would keep saying otherwise for ever.
func TestSyncReconcilesWhatAPullRequestCloses(t *testing.T) {
	st, key := newStore(t)
	ctx := context.Background()

	first := observed(key)
	first.ClosingIssues = []string{"scottlaird/roz#214", "scottlaird/roz#218"}
	if _, err := Sync(ctx, st, &fakeFetcher{
		result: github.Result{PullRequests: []github.PullRequest{first}},
	}); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	// The body is edited: one keyword goes, one arrives.
	second := observed(key)
	second.ClosingIssues = []string{"scottlaird/roz#218", "scottlaird/roz#220"}
	if _, err := Sync(ctx, st, &fakeFetcher{
		result: github.Result{PullRequests: []github.PullRequest{second}},
	}); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	want := []string{"github:scottlaird/roz#218", "github:scottlaird/roz#220"}
	if got := issuesFor(t, st, key); !equal(got, want) {
		t.Errorf("issues = %v, want %v", got, want)
	}

	// The issue itself stays. What the tracker said is not made untrue by a
	// pull request no longer claiming to close it. loadIssue fails the test if
	// it is gone.
	if issue := loadIssue(t, st, "scottlaird/roz#214"); issue.Key != "scottlaird/roz#214" {
		t.Errorf("the unlinked issue reads back as %+v", issue)
	}
}

// TestSyncLeavesAHandMadeLinkAlone is why source is in the primary key. Sync
// owns its own rows and only its own — otherwise reconciling would quietly
// delete the Jira link that is the only kind nothing will ever re-observe.
func TestSyncLeavesAHandMadeLinkAlone(t *testing.T) {
	st, key := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, store.Actor("agent:test"))
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.LinkPRIssue(ctx, key, store.TrackerJira, "CDSS-1744", store.LinkFromManual); err != nil {
		t.Fatalf("LinkPRIssue() returned error: %v", err)
	}
	// The same issue GitHub will also report, asserted by hand as well.
	if err := tx.LinkPRIssue(ctx, key, store.TrackerGitHub, "scottlaird/roz#214", store.LinkFromManual); err != nil {
		t.Fatalf("LinkPRIssue() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	pr := observed(key)
	pr.ClosingIssues = []string{"scottlaird/roz#214"}
	if _, err := Sync(ctx, st, &fakeFetcher{
		result: github.Result{PullRequests: []github.PullRequest{pr}},
	}); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	// GitHub then drops both: the body is rewritten to close nothing.
	bare := observed(key)
	bare.ClosingIssues = nil
	if _, err := Sync(ctx, st, &fakeFetcher{
		result: github.Result{PullRequests: []github.PullRequest{bare}},
	}); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	want := []string{"github:scottlaird/roz#214", "jira:CDSS-1744"}
	if got := issuesFor(t, st, key); !equal(got, want) {
		t.Errorf("issues = %v, want the hand-made links to survive: %v", got, want)
	}
}

// TestSecondSyncDoesNotRelogTheSameLinks: a sync that repeats itself must
// write nothing, or the log fills with facts that have not changed.
func TestSecondSyncDoesNotRelogTheSameLinks(t *testing.T) {
	st, key := newStore(t)
	ctx := context.Background()
	pr := observed(key)
	pr.ClosingIssues = []string{"scottlaird/roz#214"}
	client := &fakeFetcher{result: github.Result{PullRequests: []github.PullRequest{pr}}}

	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	before, err := st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq() returned error: %v", err)
	}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	after, err := st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq() returned error: %v", err)
	}
	if after != before {
		t.Errorf("the second sync wrote %d events, want none", after-before)
	}
}

func issuesFor(t *testing.T, st *store.Store, prID string) []string {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	ids, err := tx.IssueIDsForPR(ctx, prID)
	if err != nil {
		t.Fatalf("IssueIDsForPR() returned error: %v", err)
	}
	return ids
}

func equal(got, want []string) bool {
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
