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

func TestPRTrackWithAPipeline(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/todo")

	if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/todo#1",
		"--pipeline", "direct"); err != nil {
		t.Fatalf("pr track --pipeline returned error: %v", err)
	}

	out, err := runCLI(t, "pr", "show", "--db", db, "scottlaird/todo#1", "-o", "json")
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	var pr struct {
		Pipeline string `json:"pipeline"`
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		t.Fatalf("pr show -o json returned %q: %v", out, err)
	}
	if pr.Pipeline != "direct" {
		t.Errorf("pipeline = %q, want %q", pr.Pipeline, "direct")
	}
}

// TestPRTrackWithoutAPipelineStaysUnset: unset is the ordinary case and means
// the repository's, so nothing should be filled in here — unlike `repo track`,
// which resolves the default eagerly.
func TestPRTrackWithoutAPipelineStaysUnset(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/todo")

	if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/todo#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}

	out, err := runCLI(t, "pr", "show", "--db", db, "scottlaird/todo#1", "-o", "json")
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	var pr struct {
		Pipeline *string `json:"pipeline"`
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		t.Fatalf("pr show -o json returned %q: %v", out, err)
	}
	if pr.Pipeline != nil {
		t.Errorf("pipeline = %q, want it unset so the repository decides", *pr.Pipeline)
	}
}

func TestPRSet(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/todo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/todo#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}

	out, err := runCLI(t, "pr", "set", "--db", db, "scottlaird/todo#1", "--pipeline", "direct")
	if err != nil {
		t.Fatalf("pr set returned error: %v", err)
	}
	if !strings.Contains(out, "pipeline") || !strings.Contains(out, "direct") {
		t.Errorf("pr set did not report the change:\n%s", out)
	}

	// Setting it again is a no-op, because the diff decides.
	if out, err := runCLI(t, "pr", "set", "--db", db, "scottlaird/todo#1",
		"--pipeline", "direct"); err != nil {
		t.Fatalf("pr set returned error: %v", err)
	} else if !strings.Contains(out, "unchanged") {
		t.Errorf("pr set reported %q, want it unchanged", out)
	}

	// An empty value returns it to the repository's chain.
	if out, err := runCLI(t, "pr", "set", "--db", db, "scottlaird/todo#1",
		"--pipeline", ""); err != nil {
		t.Fatalf("pr set --pipeline \"\" returned error: %v", err)
	} else if !strings.Contains(out, "pipeline") {
		t.Errorf("clearing did not report the change:\n%s", out)
	}
}

func TestPRPipelineRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "an unknown pipeline at track",
			args:    []string{"pr", "track", "scottlaird/todo#2", "--pipeline", "nonsense"},
			wantErr: "is not a pipeline",
		},
		{
			name:    "nothing to set",
			args:    []string{"pr", "set", "scottlaird/todo#1"},
			wantErr: "nothing to set",
		},
		{
			name:    "sync may not write it",
			args:    []string{"pr", "set", "scottlaird/todo#1", "--pipeline", "direct", "--actor", "sync:github"},
			wantErr: "sync actors are set by",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			trackRepo(t, db, "scottlaird/todo")
			if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/todo#1"); err != nil {
				t.Fatalf("pr track returned error: %v", err)
			}

			_, err := runCLI(t, append(tt.args, "--db", db)...)
			if err == nil {
				t.Fatalf("%v was accepted, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestPRListShowsAPipelineOnlyWhenOneIsSet: an override is worth seeing, and a
// column of dashes in an already-wide table is not.
func TestPRListShowsAPipelineOnlyWhenOneIsSet(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/todo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/todo#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}

	plain, err := runCLI(t, "pr", "list", "--db", db)
	if err != nil {
		t.Fatalf("pr list returned error: %v", err)
	}
	if strings.Contains(plain, "PIPELINE") {
		t.Errorf("the column appears with nothing in it:\n%s", plain)
	}

	if _, err := runCLI(t, "pr", "set", "--db", db, "scottlaird/todo#1",
		"--pipeline", "direct"); err != nil {
		t.Fatalf("pr set returned error: %v", err)
	}
	withOne, err := runCLI(t, "pr", "list", "--db", db)
	if err != nil {
		t.Fatalf("pr list returned error: %v", err)
	}
	if !strings.Contains(withOne, "PIPELINE") || !strings.Contains(withOne, "direct") {
		t.Errorf("the override is not visible:\n%s", withOne)
	}
}
