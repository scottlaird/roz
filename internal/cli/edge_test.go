package cli

import (
	"strings"
	"testing"
)

// closeAction closes an action.
func closeAction(t *testing.T, db, id string) string {
	t.Helper()
	out, err := runCLI(t, "action", "close", "--db", db, id)
	if err != nil {
		t.Fatalf("action close returned error: %v", err)
	}
	return out
}

func TestAddBlocker(t *testing.T) {
	db := initDB(t)
	blocked := addAction(t, db, "--title", "deploy it", "--verb", "run")
	blocker := addAction(t, db, "--title", "write it", "--verb", "write")

	out, err := runCLI(t, "action", "add-blocker", "--db", db, "--from", blocked, "--to", blocker)
	if err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}
	if !strings.Contains(out, "blocked") || !strings.Contains(out, blocker) {
		t.Errorf("output does not say what it is waiting for:\n%s", out)
	}

	if got := showActionJSON(t, db, blocked)["state"]; got != "blocked" {
		t.Errorf("state = %#v, want blocked", got)
	}
}

// TestShowNamesTheBlockers: a state of blocked with no reason given is worse
// than not showing the state at all.
func TestShowNamesTheBlockers(t *testing.T) {
	db := initDB(t)
	blocked := addAction(t, db, "--title", "deploy it", "--verb", "run")
	blocker := addAction(t, db, "--title", "write it", "--verb", "write")

	if _, err := runCLI(t, "action", "add-blocker", "--db", db, "--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}

	out, err := runCLI(t, "action", "show", "--db", db, blocked)
	if err != nil {
		t.Fatalf("action show returned error: %v", err)
	}
	if !strings.Contains(out, "blocked_by") || !strings.Contains(out, blocker) {
		t.Errorf("show does not name the blocker:\n%s", out)
	}
}

func TestHideBehindAndBack(t *testing.T) {
	db := initDB(t)
	hidden := addAction(t, db, "--title", "tidy up after", "--verb", "write")
	behind := addAction(t, db, "--title", "the real work", "--verb", "write")

	if _, err := runCLI(t, "action", "hide-behind", "--db", db,
		"--action", hidden, "--behind", behind); err != nil {
		t.Fatalf("action hide-behind returned error: %v", err)
	}
	if got := showActionJSON(t, db, hidden)["hidden_behind"]; got != behind {
		t.Errorf("hidden_behind = %#v, want %q", got, behind)
	}

	// Hiding is a judgement, not a state: the action is still ready.
	if got := showActionJSON(t, db, hidden)["state"]; got != "ready" {
		t.Errorf("state = %#v, want ready — hiding is not blocking", got)
	}

	if _, err := runCLI(t, "action", "hide-behind", "--db", db,
		"--action", hidden, "--behind", ""); err != nil {
		t.Fatalf("clearing hidden_behind returned error: %v", err)
	}
	if got := showActionJSON(t, db, hidden)["hidden_behind"]; got != nil {
		t.Errorf("hidden_behind = %#v, want null after clearing", got)
	}
}

func TestLinkPR(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/roz")
	if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/roz#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	action := addAction(t, db, "--title", "write it", "--verb", "write")

	out, err := runCLI(t, "action", "link-pr", "--db", db,
		"--action", action, "--pr", "scottlaird/roz#1")
	if err != nil {
		t.Fatalf("action link-pr returned error: %v", err)
	}
	if !strings.Contains(out, "subject") {
		t.Errorf("output does not name the role:\n%s", out)
	}

	shown, err := runCLI(t, "action", "show", "--db", db, action)
	if err != nil {
		t.Fatalf("action show returned error: %v", err)
	}
	if !strings.Contains(shown, "subject_pr") || !strings.Contains(shown, "scottlaird/roz#1") {
		t.Errorf("show does not name the pull request:\n%s", shown)
	}
}

func TestEdgeRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "blocking on something unknown",
			args:    []string{"action", "add-blocker", "--from", "NA1", "--to", "NA404"},
			wantErr: "no such item: NA404",
		},
		{
			name:    "blocking itself",
			args:    []string{"action", "add-blocker", "--from", "NA1", "--to", "NA1"},
			wantErr: "cannot block itself",
		},
		{
			name:    "hiding behind something unknown",
			args:    []string{"action", "hide-behind", "--action", "NA1", "--behind", "NA404"},
			wantErr: "no such item: NA404",
		},
		{
			name:    "linking an untracked pull request",
			args:    []string{"action", "link-pr", "--action", "NA1", "--pr", "nobody/nothing#9"},
			wantErr: "no such item: nobody/nothing#9",
		},
		{
			name:    "an invented role",
			args:    []string{"action", "link-pr", "--action", "NA1", "--pr", "x#1", "--role", "adjacent"},
			wantErr: "is not a role",
		},
		{
			name:    "a sync actor",
			args:    []string{"action", "add-blocker", "--from", "NA1", "--to", "NA2", "--actor", "sync:github"},
			wantErr: "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			addAction(t, db, "--title", "an action", "--verb", "write")
			addAction(t, db, "--title", "another", "--verb", "write")

			_, err := runCLI(t, append(tt.args, "--db", db)...)
			if err == nil {
				t.Fatalf("%v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestBlockingAClosedAction: an edge onto something already finished would
// block forever, since nothing will close it a second time.
func TestBlockingAClosedAction(t *testing.T) {
	db := initDB(t)
	blocked := addAction(t, db, "--title", "deploy it", "--verb", "run")
	blocker := addAction(t, db, "--title", "write it", "--verb", "write")

	closeAction(t, db, blocker)

	_, err := runCLI(t, "action", "add-blocker", "--db", db, "--from", blocked, "--to", blocker)
	if err == nil {
		t.Fatal("blocking on a closed action was accepted, want an error")
	}
	if !strings.Contains(err.Error(), "blocks nothing") {
		t.Errorf("error = %v, want it to say the blocker is already closed", err)
	}
}
