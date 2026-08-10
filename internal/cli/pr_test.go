package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// trackRepo records a repository, which pr track requires first.
func trackRepo(t *testing.T, db, id string) {
	t.Helper()
	if _, err := runCLI(t, "repo", "track", "--db", db, id); err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}
}

func TestPRTrackAndList(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/myrepo")

	out, err := runCLI(t, "pr", "track", "--db", db, "owner/myrepo#812")
	if err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	if got := strings.TrimSpace(out); got != "owner/myrepo#812" {
		t.Errorf("pr track printed %q, want the key", got)
	}

	listed, err := runCLI(t, "pr", "list", "--db", db)
	if err != nil {
		t.Fatalf("pr list returned error: %v", err)
	}
	if !strings.Contains(listed, "owner/myrepo#812") {
		t.Errorf("pr list does not contain the tracked pull request:\n%s", listed)
	}
}

func TestPRListEmpty(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "pr", "list", "--db", db)
	if err != nil {
		t.Fatalf("pr list returned error: %v", err)
	}
	if !strings.Contains(out, "no tracked pull requests") {
		t.Errorf("empty pr list printed %q", out)
	}
}

func TestPRShowJSON(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/myrepo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/myrepo#812"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}

	out, err := runCLI(t, "pr", "show", "--db", db, "owner/myrepo#812", "-o", "json")
	if err != nil {
		t.Fatalf("pr show -o json returned error: %v", err)
	}

	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not a JSON object: %v\n%s", err, out)
	}
	if object["id"] != "owner/myrepo#812" || object["number"] != float64(812) {
		t.Errorf("pull request = %#v, want myrepo#812", object)
	}
	// frozen is a generated column and false on a fresh row.
	if object["frozen"] != false {
		t.Errorf("frozen = %#v, want false", object["frozen"])
	}
	// The empty containers come through as structures, not quoted strings.
	if _, ok := object["approvals"].([]any); !ok {
		t.Errorf("approvals = %#v, want an array", object["approvals"])
	}
}

func TestPRListJSONIsAnArray(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/myrepo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/myrepo#812"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}

	out, err := runCLI(t, "pr", "list", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("pr list -o json returned error: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	if len(got) != 1 {
		t.Errorf("got %d pull requests, want 1", len(got))
	}
}

func TestPRRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no number", args: []string{"pr", "track", "owner/myrepo"}, wantErr: "not a pull request key"},
		{name: "no owner", args: []string{"pr", "track", "myrepo#812"}, wantErr: "expected owner/name"},
		{name: "no repo at all", args: []string{"pr", "track", "#812"}, wantErr: "not a repository"},
		{name: "not a number", args: []string{"pr", "track", "owner/myrepo#abc"}, wantErr: "no pull request number"},
		{name: "two separators", args: []string{"pr", "track", "owner/my#repo#812"}, wantErr: "more than one"},
		{name: "untracked repository", args: []string{"pr", "track", "owner/unknown#1"}, wantErr: "run `todo repo track"},
		{name: "show untracked", args: []string{"pr", "show", "owner/myrepo#999"}, wantErr: "no such item"},
		{name: "sync actor", args: []string{"pr", "track", "owner/myrepo#1", "--actor", "sync:github"}, wantErr: "not allowed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			args := append(tt.args, "--db", db)

			_, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("%v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestPRTrackIsNotIdempotent(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/myrepo")

	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/myrepo#812"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	_, err := runCLI(t, "pr", "track", "--db", db, "owner/myrepo#812")
	if err == nil {
		t.Fatal("tracking twice returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "already tracked") {
		t.Errorf("error = %v, want it to say the pull request is already tracked", err)
	}
}

// TestNoteOnAPullRequest checks the log's heterogeneous subject_id end to end:
// the same command annotates TD1 and myrepo#812.
func TestNoteOnAPullRequest(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/myrepo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/myrepo#812"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}

	if _, err := runCLI(t, "note", "--db", db, "owner/myrepo#812", "stacked on 811"); err != nil {
		t.Fatalf("note on a pull request returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once", "--kind", "note", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "owner/myrepo#812") || !strings.Contains(out, "stacked on 811") {
		t.Errorf("log does not contain the note against the pull request:\n%s", out)
	}
}
