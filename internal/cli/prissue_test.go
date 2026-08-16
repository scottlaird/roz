package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/store"
)

// TestPRLinkIssueIsTheJiraPath. Nothing reads Jira, and a Jira key is not in a
// field GitHub reports, so a hand-made link is the only way this association
// can exist at all.
func TestPRLinkIssueIsTheJiraPath(t *testing.T) {
	db, pr := trackedPR(t)

	out, err := runCLI(t, "pr", "link-issue", pr, "--db", db,
		"--issue", "CDSS-1744", "--actor", "agent:test")
	if err != nil {
		t.Fatalf("pr link-issue returned error: %v", err)
	}
	if !strings.Contains(out, "jira:CDSS-1744") {
		t.Errorf("link did not report what it linked: %s", out)
	}

	shown, err := runCLI(t, "pr", "show", pr, "--db", db)
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	if !strings.Contains(shown, "jira:CDSS-1744") {
		t.Errorf("pr show does not name the issue:\n%s", shown)
	}
}

// TestPRUnlinkIssueRefusesGitHubsOwn. Sync would restore it on the next cycle,
// and an unlink that silently undid itself is worse than one that says why.
func TestPRUnlinkIssueRefusesGitHubsOwn(t *testing.T) {
	db, pr := trackedPR(t)

	// Stand in for what sync writes, through the same store call it uses.
	linkBySync(t, db, pr, "scottlaird/roz#214")

	_, err := runCLI(t, "pr", "unlink-issue", pr, "--db", db,
		"--issue", "scottlaird/roz#214", "--tracker", "github", "--actor", "agent:test")
	if err == nil {
		t.Fatal("unlinking GitHub's own link returned nil, want an error")
	}
	for _, want := range []string{"by GitHub", "pull request body"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %q", err, want)
		}
	}
}

// TestPRUnlinkIssueRemovesAHandMadeOne, and says so in a way that distinguishes
// it from the refusal above.
func TestPRUnlinkIssueRemovesAHandMadeOne(t *testing.T) {
	db, pr := trackedPR(t)

	if _, err := runCLI(t, "pr", "link-issue", pr, "--db", db,
		"--issue", "CDSS-1744", "--actor", "agent:test"); err != nil {
		t.Fatalf("pr link-issue returned error: %v", err)
	}
	if _, err := runCLI(t, "pr", "unlink-issue", pr, "--db", db,
		"--issue", "CDSS-1744", "--actor", "agent:test"); err != nil {
		t.Fatalf("pr unlink-issue returned error: %v", err)
	}

	shown, err := runCLI(t, "pr", "show", pr, "--db", db)
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	if strings.Contains(shown, "CDSS-1744") {
		t.Errorf("the link survived being removed:\n%s", shown)
	}

	// Removing it twice is an error rather than a silent success: the second
	// caller believes they removed something.
	_, err = runCLI(t, "pr", "unlink-issue", pr, "--db", db,
		"--issue", "CDSS-1744", "--actor", "agent:test")
	if err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Errorf("second unlink returned %v, want a not-linked error", err)
	}
}

// TestPRLinkIssueRefusesAnUntrackedPullRequest, by name rather than by foreign
// key, which is the difference between a message and a constraint failure.
func TestPRLinkIssueRefusesAnUntrackedPullRequest(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "pr", "link-issue", "example/nope#9", "--db", db,
		"--issue", "CDSS-1744", "--actor", "agent:test")
	if err == nil {
		t.Fatal("linking an untracked pull request returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "example/nope#9") {
		t.Errorf("error = %v, want it to name the pull request", err)
	}
}

// linkBySync writes the row sync would write, through the same store call, so
// the refusal is tested against a real github-sourced link rather than a
// hand-made one relabelled.
func linkBySync(t *testing.T, db, prID, key string) {
	t.Helper()
	ctx := context.Background()

	st, err := store.OpenStore(db)
	if err != nil {
		t.Fatalf("opening %s: %v", db, err)
	}
	defer st.Close()

	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.ReconcileClosingIssues(ctx, prID, []string{key}); err != nil {
		t.Fatalf("ReconcileClosingIssues() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}
