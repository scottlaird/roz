package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/scottlaird/todo/internal/ghsync"
	"github.com/scottlaird/todo/internal/github"
)

// stubFetcher stands in for GitHub so these tests touch no network.
type stubFetcher struct {
	result github.Result
	err    error
}

func (s stubFetcher) Fetch(context.Context, []string) (github.Result, error) {
	if s.err != nil {
		return github.Result{}, s.err
	}
	return s.result, nil
}

// withFetcher swaps the client the command builds, and puts it back after.
func withFetcher(t *testing.T, f ghsync.Fetcher) {
	t.Helper()
	original := newFetcher
	newFetcher = func() ghsync.Fetcher { return f }
	t.Cleanup(func() { newFetcher = original })
}

// trackedPR returns a database with one repository and one pull request.
func trackedPR(t *testing.T) (db, key string) {
	t.Helper()
	db = initDB(t)
	trackRepo(t, db, "owner/repo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	return db, "owner/repo#1"
}

func TestSyncGitHubReportsChanges(t *testing.T) {
	db, key := trackedPR(t)
	withFetcher(t, stubFetcher{result: github.Result{
		PullRequests: []github.PullRequest{{
			Key: key, Repo: "owner/repo", Number: 1,
			Title: "a title", State: "OPEN", BaseRef: "main",
			ReviewDecision: "APPROVED",
		}},
	}})

	out, err := runCLI(t, "sync", "github", "--db", db)
	if err != nil {
		t.Fatalf("sync github returned error: %v", err)
	}
	for _, want := range []string{key, "title", "APPROVED", "polled 1, 1 changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}

	// The state really landed.
	shown, err := runCLI(t, "pr", "show", "--db", db, key)
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	if !strings.Contains(shown, "APPROVED") {
		t.Errorf("pr show does not reflect the sync:\n%s", shown)
	}
}

func TestSyncGitHubIsQuietWhenNothingMoved(t *testing.T) {
	db, key := trackedPR(t)
	fetcher := stubFetcher{result: github.Result{
		PullRequests: []github.PullRequest{{
			Key: key, Repo: "owner/repo", Number: 1, Title: "a title", State: "OPEN",
		}},
	}}
	withFetcher(t, fetcher)

	if _, err := runCLI(t, "sync", "github", "--db", db); err != nil {
		t.Fatalf("first sync returned error: %v", err)
	}
	out, err := runCLI(t, "sync", "github", "--db", db)
	if err != nil {
		t.Fatalf("second sync returned error: %v", err)
	}
	if !strings.Contains(out, "polled 1, 0 changed") {
		t.Errorf("second sync reported %q, want nothing changed", out)
	}
}

func TestSyncGitHubReportsUnreadable(t *testing.T) {
	db, key := trackedPR(t)
	withFetcher(t, stubFetcher{result: github.Result{
		Missing: map[string]string{key: "is not a pull request GitHub will show us"},
	}})

	out, err := runCLI(t, "sync", "github", "--db", db)
	if err != nil {
		t.Fatalf("sync github returned error: %v", err)
	}
	if !strings.Contains(out, "could not be read") || !strings.Contains(out, "1 unreadable") {
		t.Errorf("output does not report the unreadable pull request:\n%s", out)
	}

	// It reaches the log as an exception, where a monitor would find it.
	log, err := runCLI(t, "watch", "--db", db, "--once", "--severity", "exception", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(log, "pr_unresolvable") {
		t.Errorf("no exception was logged:\n%s", log)
	}
}

func TestSyncGitHubSurfacesTransportFailure(t *testing.T) {
	db, _ := trackedPR(t)
	withFetcher(t, stubFetcher{err: errors.New("gh: not authenticated")})

	_, err := runCLI(t, "sync", "github", "--db", db)
	if err == nil {
		t.Fatal("sync with a failing client returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("error = %v, want it to carry the cause", err)
	}
}

func TestSyncRejections(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		wantErr string
	}{
		{name: "planned but unbuilt", source: "jira", wantErr: "not built yet"},
		{name: "also planned", source: "slack", wantErr: "not built yet"},
		{name: "nonsense", source: "gitlab", wantErr: "is not a source"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			_, err := runCLI(t, "sync", tt.source, "--db", db)
			if err == nil {
				t.Fatalf("sync %s returned nil, want an error", tt.source)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSyncWithNothingTracked(t *testing.T) {
	db := initDB(t)
	withFetcher(t, stubFetcher{})

	out, err := runCLI(t, "sync", "github", "--db", db)
	if err != nil {
		t.Fatalf("sync github returned error: %v", err)
	}
	if !strings.Contains(out, "polled 0, 0 changed") {
		t.Errorf("output = %q, want it to report nothing polled", out)
	}
}

// TestSyncClosesTheStepsGitHubFinished is the whole loop end to end: a write
// action closes into a chain, GitHub reports the pull request merged, and the
// steps close themselves.
func TestSyncClosesTheStepsGitHubFinished(t *testing.T) {
	db, key := trackedPR(t)
	if _, err := runCLI(t, "repo", "set", "--db", db, "owner/repo", "--pipeline", "direct"); err != nil {
		t.Fatalf("repo set returned error: %v", err)
	}

	write := addAction(t, db, "--title", "write it", "--verb", "write")
	if _, err := runCLI(t, "action", "close", "--db", db, write, "--pr", key); err != nil {
		t.Fatalf("action close returned error: %v", err)
	}

	// direct is undraft → merge, and GitHub says both are done.
	withFetcher(t, stubFetcher{result: github.Result{
		PullRequests: []github.PullRequest{{
			Key: key, Repo: "owner/repo", Number: 1,
			Title: "a title", State: "MERGED", BaseRef: "main", IsDraft: false,
		}},
	}})

	out, err := runCLI(t, "sync", "github", "--db", db)
	if err != nil {
		t.Fatalf("sync github returned error: %v", err)
	}
	if !strings.Contains(out, "closed") {
		t.Errorf("sync did not report closing anything:\n%s", out)
	}

	listed, err := runCLI(t, "action", "list", "--db", db, "--open")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(listed, "no actions") {
		t.Errorf("actions are still open after GitHub finished the work:\n%s", listed)
	}
}

// TestSettledIsReportedEvenWhenQuiet: an action closing without anyone asking
// is the most surprising thing sync does, and --quiet is about noise, not
// about hiding that.
func TestSettledIsReportedEvenWhenQuiet(t *testing.T) {
	db, key := trackedPR(t)
	if _, err := runCLI(t, "repo", "set", "--db", db, "owner/repo", "--pipeline", "direct"); err != nil {
		t.Fatalf("repo set returned error: %v", err)
	}
	write := addAction(t, db, "--title", "write it", "--verb", "write")
	if _, err := runCLI(t, "action", "close", "--db", db, write, "--pr", key); err != nil {
		t.Fatalf("action close returned error: %v", err)
	}

	withFetcher(t, stubFetcher{result: github.Result{
		PullRequests: []github.PullRequest{{
			Key: key, Repo: "owner/repo", Number: 1, State: "MERGED", BaseRef: "main",
		}},
	}})

	out, err := runCLI(t, "sync", "github", "--db", db, "--quiet")
	if err != nil {
		t.Fatalf("sync github --quiet returned error: %v", err)
	}
	if !strings.Contains(out, "closed") {
		t.Errorf("--quiet hid a self-closing action:\n%s", out)
	}
	if strings.Contains(out, "polled") {
		t.Errorf("--quiet printed the summary:\n%s", out)
	}
}
