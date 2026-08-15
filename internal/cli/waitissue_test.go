package cli

import (
	"strings"
	"testing"
)

// closeIssue records that a tracker said an issue closed.
func closeIssue(t *testing.T, db, key, at string) string {
	t.Helper()
	out, err := runCLI(t, "issue", "observe", "--db", db, key,
		"--tracker", "github", "--status", "CLOSED", "--closed-at", at)
	if err != nil {
		t.Fatalf("issue observe returned error: %v", err)
	}
	return out
}

// TestAWaitOnAnIssueClosesWhenItDoes is the gap: work blocked on something
// that is not ours. Until now that was a plain wait nobody could close except
// by hand, sitting in the queue looking live.
func TestAWaitOnAnIssueClosesWhenItDoes(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "wait for the upstream fix", "--verb", "wait_issue",
		"--tracker", "github", "--issue", "rust-lang/rust#1")

	if got := showActionJSON(t, db, id)["state"]; got != "ready" {
		t.Fatalf("state = %#v before the issue closed, want ready", got)
	}

	out := closeIssue(t, db, "rust-lang/rust#1", "2026-08-14")
	if !strings.Contains(out, id) {
		t.Errorf("observing the close did not settle the wait:\n%s", out)
	}
	if got := showActionJSON(t, db, id)["state"]; got != "done" {
		t.Errorf("state = %#v, want done", got)
	}
}

// TestAnOpenIssueClosesNothing: absence is not completion, and neither is a
// status nobody has read.
func TestAnOpenIssueClosesNothing(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "wait for the upstream fix", "--verb", "wait_issue",
		"--tracker", "github", "--issue", "rust-lang/rust#1")

	// A status and no closing time. The predicate reads the time, because
	// "Done", "Closed" and "Resolved" are three trackers' words for one idea
	// and choosing between them is not roz's to do.
	if _, err := runCLI(t, "issue", "observe", "--db", db, "rust-lang/rust#1",
		"--tracker", "github", "--status", "In Progress"); err != nil {
		t.Fatalf("issue observe returned error: %v", err)
	}
	if got := showActionJSON(t, db, id)["state"]; got != "ready" {
		t.Errorf("state = %#v, want ready", got)
	}
}

// TestAWaitWrittenAfterTheCloseSettlesAtOnce: the same staleness `link-pr`
// had. The fact is already stored, so waiting for the next poll would be the
// queue disagreeing with what it already knows.
func TestAWaitWrittenAfterTheCloseSettlesAtOnce(t *testing.T) {
	db := initDB(t)
	closeIssue(t, db, "rust-lang/rust#1", "2026-08-14")

	id := addAction(t, db, "--title", "wait for it", "--verb", "wait_issue",
		"--tracker", "github", "--issue", "rust-lang/rust#1")

	if got := showActionJSON(t, db, id)["state"]; got != "done" {
		t.Errorf("state = %#v, want done on the spot", got)
	}
}

// TestAWaitIssueVerbNeedsAnIssue: with nothing to ask, the answer is false for
// ever — correctly, since absence is not completion — and the action sits in
// the queue with nothing on it saying why it will not move.
func TestAWaitIssueVerbNeedsAnIssue(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "wait for something", "--verb", "wait_issue")
	if err == nil {
		t.Fatal("action add accepted a wait_issue with nothing to wait for")
	}
	if !strings.Contains(err.Error(), "--issue") {
		t.Errorf("error does not offer the remedy: %v", err)
	}
}

// TestMovingOntoWaitIssueNeedsOneToo: the same requirement from the other
// side, with the remedy `action set` actually has.
func TestMovingOntoWaitIssueNeedsOneToo(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "wait for it", "--verb", "investigate")

	_, err := runCLI(t, "action", "set", "--db", db, id, "--verb", "wait_issue")
	if err == nil {
		t.Fatal("action set moved an action onto wait_issue with nothing to wait for")
	}
	if !strings.Contains(err.Error(), "wait-issue") {
		t.Errorf("error does not name the command that fixes it: %v", err)
	}

	// And with one named, the move is allowed.
	if _, err := runCLI(t, "action", "wait-issue", "--db", db,
		"--action", id, "--tracker", "github", "--issue", "rust-lang/rust#1"); err != nil {
		t.Fatalf("action wait-issue returned error: %v", err)
	}
	if _, err := runCLI(t, "action", "set", "--db", db, id, "--verb", "wait_issue"); err != nil {
		t.Errorf("action set refused a verb whose issue is named: %v", err)
	}
}

// TestWaitingOnAnIssuePutsItInThePoll: sync reads the issues roz has rows for,
// so a wait on one nothing had recorded would never come true.
func TestWaitingOnAnIssuePutsItInThePoll(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "wait for the upstream fix", "--verb", "wait_issue",
		"--tracker", "github", "--issue", "rust-lang/rust#1")

	out, err := runCLI(t, "issue", "list", "--db", db)
	if err != nil {
		t.Fatalf("issue list returned error: %v", err)
	}
	if !strings.Contains(out, "rust-lang/rust#1") {
		t.Errorf("the issue waited on is not recorded:\n%s", out)
	}
}
