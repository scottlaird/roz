package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI executes the command tree with args and returns everything it wrote.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	err := root.Execute()
	return out.String(), err
}

// initDB returns the path of a freshly initialised database.
func initDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roz.db")
	if _, err := runCLI(t, "init", "--db", path); err != nil {
		t.Fatalf("init returned error: %v", err)
	}
	return path
}

func TestProjectAddPrintsID(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "project", "add", "--db", db, "--title", "first")
	if err != nil {
		t.Fatalf("project add returned error: %v", err)
	}
	if got, want := strings.TrimSpace(out), "ROZ1"; got != want {
		t.Errorf("project add printed %q, want %q", got, want)
	}
}

func TestProjectAddThenList(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "project", "add", "--db", db,
		"--title", "Split the nodepool", "--priority", "1", "--effort", "weeks"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}

	out, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	for _, want := range []string{"ROZ1", "active", "weeks", "Split the nodepool"} {
		if !strings.Contains(out, want) {
			t.Errorf("project list output does not contain %q:\n%s", want, out)
		}
	}
}

func TestProjectListEmpty(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	if !strings.Contains(out, "no projects") {
		t.Errorf("project list on an empty database printed %q", out)
	}
}

// TestFlagOverridesJSON pins the documented precedence: --json is the base an
// agent generates, and an explicit flag wins over it.
func TestFlagOverridesJSON(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "project", "add", "--db", db, "--title", "t",
		"--json", `{"priority":4,"effort":"days"}`, "--priority", "2")
	if err != nil {
		t.Fatalf("project add returned error: %v", err)
	}

	out, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	// The flag wins on priority; the JSON still supplies effort.
	if !strings.Contains(out, "2") || !strings.Contains(out, "days") {
		t.Errorf("want priority 2 from the flag and effort days from --json:\n%s", out)
	}
}

func TestProjectListJSON(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "project", "add", "--db", db,
		"--title", "with refs", "--priority", "2",
		"--design-ref", "docs/a.md", "--design-ref", "docs/b.md"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}

	out, err := runCLI(t, "project", "list", "--db", db, "--output", "json")
	if err != nil {
		t.Fatalf("project list --output json returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("got %d projects, want 1", len(got))
	}

	project := got[0]
	if project["id"] != "ROZ1" || project["title"] != "with refs" {
		t.Errorf("project = %#v, want ROZ1 / with refs", project)
	}
	if project["priority"] != float64(2) {
		t.Errorf("priority = %#v, want 2", project["priority"])
	}
	if project["effort"] != nil {
		t.Errorf("effort = %#v, want null", project["effort"])
	}
	refs, ok := project["design_refs"].([]any)
	if !ok || len(refs) != 2 {
		t.Errorf("design_refs = %#v, want an array of two paths", project["design_refs"])
	}
}

func TestProjectListJSONEmpty(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "project", "list", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("project list -o json returned error: %v", err)
	}
	if got := strings.TrimSpace(out); got != "[]" {
		t.Errorf("empty list printed %q, want []", got)
	}
}

func TestProjectListRejectsUnknownOutput(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "project", "list", "--db", db, "-o", "yaml")
	if err == nil {
		t.Fatal("project list -o yaml returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "not recognised") {
		t.Errorf("error = %v, want it to reject the format", err)
	}
}

func TestProjectAddRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "sync actor",
			args:    []string{"--actor", "sync:github"},
			wantErr: "not allowed",
		},
		{
			name:    "unrecognised actor",
			args:    []string{"--actor", "robot"},
			wantErr: "not recognised",
		},
		{
			name:    "observed column via json",
			args:    []string{"--json", `{"last_verified_at":"2026-08-10T00:00:00.000Z"}`},
			wantErr: "observed",
		},
		{
			name:    "unknown column via json",
			args:    []string{"--json", `{"titel":"typo"}`},
			wantErr: `no column "titel"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			args := append([]string{"project", "add", "--db", db, "--title", "t"}, tt.args...)

			_, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("project add %v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestAgentActorIsAccepted(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "project", "add", "--db", db,
		"--title", "by an agent", "--actor", "agent:claude"); err != nil {
		t.Errorf("project add --actor agent:claude returned error: %v", err)
	}
}

// TestUninitialisedDatabaseAdvises checks the error points somewhere useful
// rather than reporting a missing table.
func TestUninitialisedDatabaseAdvises(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-here.db")

	_, err := runCLI(t, "project", "list", "--db", missing)
	if err == nil {
		t.Fatal("project list on a missing database returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "roz init") {
		t.Errorf("error = %v, want it to suggest `roz init`", err)
	}
}

// TestRejectedInputConsumesNoIdentifier checks that validation happens before
// allocation. Gaps are acceptable, but a typo should not cost a number.
func TestRejectedInputConsumesNoIdentifier(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "project", "add", "--db", db,
		"--title", "t", "--json", `{"titel":"typo"}`); err == nil {
		t.Fatal("project add with a bad column returned nil, want an error")
	}

	out, err := runCLI(t, "project", "add", "--db", db, "--title", "first real one")
	if err != nil {
		t.Fatalf("project add returned error: %v", err)
	}
	if got, want := strings.TrimSpace(out), "ROZ1"; got != want {
		t.Errorf("first successful add produced %q, want %q", got, want)
	}
}

// TestProjectClose is the verb the queue needed: a closed project must not
// leave work in it.
func TestProjectClose(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "Split the nodepool")

	first := addAction(t, db, "--title", "write it", "--verb", "write", "--project", project)
	waiting := addAction(t, db, "--title", "waits on it", "--verb", "announce")
	if _, err := runCLI(t, "action", "add-blocker", "--db", db, "--from", waiting, "--to", first); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}

	out, err := runCLI(t, "project", "close", "--db", db, project, "--status", "retired")
	if err != nil {
		t.Fatalf("project close returned error: %v", err)
	}
	for _, want := range []string{"retired", first + " dropped", waiting + " is now ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}

	// The queue is left with only the thing that is still worth doing.
	listed, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if strings.Contains(listed, "write it") {
		t.Errorf("a dropped action is still in the queue:\n%s", listed)
	}
	if !strings.Contains(listed, "waits on it") {
		t.Errorf("the freed action is missing from the queue:\n%s", listed)
	}
}

func TestProjectCloseRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unknown project",
			args:    []string{"project", "close", "ROZ404"},
			wantErr: "no such item: ROZ404",
		},
		{
			name:    "superseding through close",
			args:    []string{"project", "close", "ROZ1", "--status", "superseded"},
			wantErr: "supersede",
		},
		{
			name:    "an invented status",
			args:    []string{"project", "close", "ROZ1", "--status", "abandoned"},
			wantErr: "does not close a project",
		},
		{
			name:    "a sync actor",
			args:    []string{"project", "close", "ROZ1", "--actor", "sync:github"},
			wantErr: "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			addProject(t, db, "Split the nodepool")

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
