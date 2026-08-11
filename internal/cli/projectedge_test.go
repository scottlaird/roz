package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectBlockAndClose(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "thread the flag through")
	blocked := addProject(t, db, "serve MCP over HTTP")

	out, err := runCLI(t, "project", "block", "--db", db, "--from", blocked, "--to", blocker)
	if err != nil {
		t.Fatalf("project block returned error: %v", err)
	}
	if !strings.Contains(out, "blocked") || !strings.Contains(out, blocker) {
		t.Errorf("block did not report what happened:\n%s", out)
	}

	// The status moved with the edge, so `blocked` finally means something.
	listed, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	if !strings.Contains(listed, "blocked") {
		t.Errorf("the project is not shown as blocked:\n%s", listed)
	}

	closed, err := runCLI(t, "project", "close", "--db", db, blocker, "--status", "done")
	if err != nil {
		t.Fatalf("project close returned error: %v", err)
	}
	if !strings.Contains(closed, blocked) || !strings.Contains(closed, "active") {
		t.Errorf("closing the blocker did not report freeing %s:\n%s", blocked, closed)
	}
}

func TestProjectUnblock(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "thread the flag through")
	blocked := addProject(t, db, "serve MCP over HTTP")

	if _, err := runCLI(t, "project", "block", "--db", db, "--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}
	out, err := runCLI(t, "project", "unblock", "--db", db, "--from", blocked, "--to", blocker)
	if err != nil {
		t.Fatalf("project unblock returned error: %v", err)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("unblock did not return it to active:\n%s", out)
	}
}

// TestBlockedProjectIsOnThePage is the reason this was worth doing now: a
// blocked project used to vanish, because the page filtered to active.
func TestBlockedProjectIsOnThePage(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "thread the flag through")
	blocked := addProject(t, db, "serve MCP over HTTP")
	if _, err := runCLI(t, "project", "block", "--db", db, "--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	page, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(page, "serve MCP over HTTP") {
		t.Errorf("the blocked project is missing from the page:\n%s", page)
	}
	if !strings.Contains(page, "blocked") {
		t.Errorf("the page does not say it is blocked:\n%s", page)
	}
}

func TestProjectBlockShowsInBothFormats(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "thread the flag through")
	blocked := addProject(t, db, "serve MCP over HTTP")
	if _, err := runCLI(t, "project", "block", "--db", db, "--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	table, err := runCLI(t, "project", "show", "--db", db, blocked)
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	if !strings.Contains(table, "blocked_by") || !strings.Contains(table, blocker) {
		t.Errorf("show does not name the blocker:\n%s", table)
	}

	encoded, err := runCLI(t, "project", "show", "--db", db, blocker, "-o", "json")
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(encoded), &object); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	blocking, _ := object["blocking"].([]any)
	if len(blocking) != 1 || blocking[0] != blocked {
		t.Errorf("blocking = %v, want [%s]", object["blocking"], blocked)
	}
}

func TestProjectBlockRejections(t *testing.T) {
	db := initDB(t)
	first := addProject(t, db, "first")
	second := addProject(t, db, "second")
	if _, err := runCLI(t, "project", "block", "--db", db, "--from", second, "--to", first); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"a cycle", []string{"project", "block", "--from", first, "--to", second}, "never unblock"},
		{"itself", []string{"project", "block", "--from", first, "--to", first}, "cannot block itself"},
		{"an unknown project", []string{"project", "block", "--from", "ROZ99", "--to", first}, "ROZ99"},
		{"an edge that is not there", []string{"project", "unblock", "--from", first, "--to", second}, "does not block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
