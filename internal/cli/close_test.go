package cli

import (
	"strings"
	"testing"
)

func TestActionClose(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "decide the shape", "--verb", "decide")

	out := closeAction(t, db, id)
	if !strings.Contains(out, "done") || !strings.Contains(out, "completed") {
		t.Errorf("close did not report what it did:\n%s", out)
	}
	if got := showActionJSON(t, db, id)["state"]; got != "done" {
		t.Errorf("state = %#v, want done", got)
	}
}

func TestActionCloseDropped(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "the thing nobody wants", "--verb", "decide")

	if _, err := runCLI(t, "action", "close", "--db", db, id, "--reason", "dropped"); err != nil {
		t.Fatalf("action close returned error: %v", err)
	}
	object := showActionJSON(t, db, id)
	if object["state"] != "dropped" || object["closed_reason"] != "dropped" {
		t.Errorf("action = %#v, want dropped, not done", object)
	}
}

// TestCloseCascadeIsReported: a close that quietly created four actions
// somewhere else would be indistinguishable from a bug.
func TestCloseCascadeIsReported(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/roz")
	if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/roz#27"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}

	write := addAction(t, db, "--title", "write the endpoint", "--verb", "write")
	dependent := addAction(t, db, "--title", "deploy it", "--verb", "run")
	hidden := addAction(t, db, "--title", "tidy up after", "--verb", "write")

	if _, err := runCLI(t, "action", "add-blocker", "--db", db, "--from", dependent, "--to", write); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}
	if _, err := runCLI(t, "action", "hide-behind", "--db", db, "--action", hidden, "--behind", write); err != nil {
		t.Fatalf("action hide-behind returned error: %v", err)
	}

	out, err := runCLI(t, "action", "close", "--db", db, write, "--pr", "scottlaird/roz#27")
	if err != nil {
		t.Fatalf("action close returned error: %v", err)
	}

	for _, want := range []string{
		"created", "un-draft", "send for review", "wait for review", "merge",
		dependent + " is now ready", hidden + " is no longer hidden",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("close output does not mention %q:\n%s", want, out)
		}
	}

	// The chain is real, not just printed.
	listed, err := runCLI(t, "action", "list", "--db", db, "--status", "blocked")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if strings.Count(listed, "\n") != 4 { // header plus three waiting steps
		t.Errorf("want three blocked steps:\n%s", listed)
	}
}

// TestCloseWithoutARepoPipelineSaysNothingWasCreated: tracking a repository
// directly leaves its pipeline unstated, and nothing should be guessed.
func TestCloseWithoutARepoPipeline(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/roz", "--pipeline", ""); err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}
	if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/roz#27"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	id := addAction(t, db, "--title", "write the endpoint", "--verb", "write")

	out, err := runCLI(t, "action", "close", "--db", db, id, "--pr", "scottlaird/roz#27")
	if err != nil {
		t.Fatalf("action close returned error: %v", err)
	}
	if strings.Contains(out, "created") {
		t.Errorf("a chain was instantiated with no pipeline stated:\n%s", out)
	}
}

func TestCloseRejectionsThroughTheCLI(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unknown action",
			args:    []string{"action", "close", "NA404"},
			wantErr: "no such item: NA404",
		},
		{
			name:    "invented reason",
			args:    []string{"action", "close", "NA1", "--reason", "bored"},
			wantErr: "is not a reason to close",
		},
		{
			name:    "untracked pull request",
			args:    []string{"action", "close", "NA1", "--pr", "nobody/nothing#9"},
			wantErr: "not tracked",
		},
		{
			name:    "sync actor",
			args:    []string{"action", "close", "NA1", "--actor", "sync:github"},
			wantErr: "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			addAction(t, db, "--title", "an action", "--verb", "write")

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

// TestClosingTwiceIsRefused: the second close would otherwise run the cascade
// again and instantiate a duplicate chain.
func TestClosingTwiceIsRefused(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "decide the shape", "--verb", "decide")
	closeAction(t, db, id)

	_, err := runCLI(t, "action", "close", "--db", db, id)
	if err == nil {
		t.Fatal("closing twice was accepted, want an error")
	}
	if !strings.Contains(err.Error(), "already done") {
		t.Errorf("error = %v, want it to say the action is already closed", err)
	}
}
