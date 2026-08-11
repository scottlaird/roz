package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/scottlaird/todo/internal/store"
)

// TestDefaultPrefixes pins what a fresh database issues, since the prefixes
// are chosen once and cannot be changed afterwards without orphaning every
// identifier already written down.
func TestDefaultPrefixes(t *testing.T) {
	db := initDB(t)

	if got := addProject(t, db, "the first one"); got != "TD1" {
		t.Errorf("project add printed %q, want TD1", got)
	}
	if got := addAction(t, db, "--title", "the first one", "--verb", "write"); got != "NA1" {
		t.Errorf("action add printed %q, want NA1", got)
	}
}

// TestPrefixesAreNotHardcoded: the defaults are defaults, and a database that
// chose otherwise must keep working. Everything but `init` reads them back
// out of the database.
func TestPrefixesAreNotHardcoded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todo.db")
	if _, err := runCLI(t, "init", "--db", path,
		"--project-prefix", "PRJ", "--action-prefix", "ACT"); err != nil {
		t.Fatalf("init returned error: %v", err)
	}

	if got := addProject(t, path, "the first one"); got != "PRJ1" {
		t.Errorf("project add printed %q, want PRJ1", got)
	}
	if got := addAction(t, path, "--title", "the first one", "--verb", "write"); got != "ACT1" {
		t.Errorf("action add printed %q, want ACT1", got)
	}

	// And the registry is what reports an unknown prefix, not a hardcoded list.
	_, err := runCLI(t, "note", "--db", path, "TD9", "nope")
	if err == nil {
		t.Fatal("note on an unknown prefix returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "project=PRJ") {
		t.Errorf("error = %v, want it to name this database's prefixes", err)
	}
}

// TestInitIsSafeToRerun: it is also the migration path, so it has to be.
func TestInitIsSafeToRerun(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "the first one")

	out, err := runCLI(t, "init", "--db", db)
	if err != nil {
		t.Fatalf("second init returned error: %v", err)
	}
	if !strings.Contains(out, "up to date") {
		t.Errorf("second init said %q, want it to report the state", out)
	}
	if !strings.Contains(out, "schema ") {
		t.Errorf("second init said %q, want it to name the schema version", out)
	}

	// Re-running init must not have moved the prefixes or the counter.
	if got := addProject(t, db, "the second one"); got != "TD2" {
		t.Errorf("project add printed %q after a re-init, want TD2 (was %s)", got, id)
	}
}

// TestInitMessage covers the three states init can leave behind. Only the
// unchanged one is reachable from the CLI without an out-of-date database, so
// the wording is checked here rather than through the command.
func TestInitMessage(t *testing.T) {
	prefixes := map[store.Entity]string{
		store.EntityProject: "TD",
		store.EntityAction:  "NA",
	}

	for _, tc := range []struct {
		name   string
		result store.InitResult
		want   string
	}{
		{
			name:   "created",
			result: store.InitResult{Created: true, From: 0, To: 15, Prefixes: prefixes},
			want:   "initialised /tmp/todo.db (schema 15, action=NA, project=TD)",
		},
		{
			name:   "migrated",
			result: store.InitResult{From: 13, To: 15, Prefixes: prefixes},
			want:   "migrated /tmp/todo.db: schema 13 → 15 (action=NA, project=TD)",
		},
		{
			name:   "unchanged",
			result: store.InitResult{From: 15, To: 15, Prefixes: prefixes},
			want:   "/tmp/todo.db is up to date (schema 15, action=NA, project=TD)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := initMessage("/tmp/todo.db", tc.result); got != tc.want {
				t.Errorf("initMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}
