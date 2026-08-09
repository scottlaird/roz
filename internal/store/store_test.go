package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newDBPath returns a path inside a fresh temp dir, with a missing parent
// directory so Init has to create one.
func newDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sub", "todo.db")
}

func TestInitCreatesDatabase(t *testing.T) {
	path := newDBPath(t)

	created, err := Init(path)
	if err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	if want := true; created != want {
		t.Errorf("Init() created = %v, want %v", created, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	if got, want := userVersionOf(t, db), schemaVersion; got != want {
		t.Errorf("user_version = %d, want %d", got, want)
	}
	for _, table := range []string{"project", "action", "pr", "event", "sequence", "actionverb"} {
		if !tableExists(t, db, table) {
			t.Errorf("table %q missing after Init()", table)
		}
	}
}

// TestInitPreservesExistingData is the contract that matters: re-running init
// must never destroy a database.
func TestInitPreservesExistingData(t *testing.T) {
	path := newDBPath(t)

	if _, err := Init(path); err != nil {
		t.Fatalf("first Init() returned error: %v", err)
	}
	writeSequenceRow(t, path, "NA", 42)

	created, err := Init(path)
	if err != nil {
		t.Fatalf("second Init() returned error: %v", err)
	}
	if want := false; created != want {
		t.Errorf("second Init() created = %v, want %v", created, want)
	}

	if got, want := readSequenceRow(t, path, "NA"), 42; got != want {
		t.Errorf("sequence next_n for NA = %d, want %d", got, want)
	}
}

func TestInitRejectsUnknownSchemaVersion(t *testing.T) {
	path := newDBPath(t)

	if _, err := Init(path); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	setUserVersion(t, path, schemaVersion+1)

	if _, err := Init(path); err == nil {
		t.Error("Init() on a newer schema version returned nil, want an error")
	}
}

func TestInitCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "todo.db")

	if _, err := Init(path); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent directory not created: %v", err)
	}
}

// TestConnectionPragmas guards the reason the pragmas live in the DSN rather
// than in schema.sql: they must hold on a connection Init did not open.
func TestConnectionPragmas(t *testing.T) {
	path := newDBPath(t)
	if _, err := Init(path); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	tests := []struct {
		pragma string
		want   string
	}{
		{"foreign_keys", "1"},
		{"journal_mode", "wal"},
		{"busy_timeout", "5000"},
	}
	for _, tt := range tests {
		t.Run(tt.pragma, func(t *testing.T) {
			var got string
			if err := db.QueryRow("PRAGMA " + tt.pragma).Scan(&got); err != nil {
				t.Fatalf("reading pragma: %v", err)
			}
			if got != tt.want {
				t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
			}
		})
	}
}

// TestForeignKeysEnforced checks the pragma has teeth, not just the value.
func TestForeignKeysEnforced(t *testing.T) {
	path := newDBPath(t)
	if _, err := Init(path); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`INSERT INTO action (id, kind, n, title, verb, state, created_at, updated_at)
	                  VALUES ('NA1', 'NA', 1, 't', 'nonexistent-verb', 'ready', '', '')`)
	if err == nil {
		t.Fatal("insert with a dangling verb reference succeeded, want a foreign key error")
	}
	// Assert the reason: the row also has to satisfy several CHECK
	// constraints, and this test is worthless if it passes on one of those.
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("insert failed with %v, want a FOREIGN KEY constraint error", err)
	}
}

func TestOpenRejectsNonDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-db")
	if err := os.WriteFile(path, []byte("this is not a SQLite file"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := Init(path); err == nil {
		t.Error("Init() on a non-database file returned nil, want an error")
	}
}

func userVersionOf(t *testing.T, db *sql.DB) int {
	t.Helper()
	version, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion() returned error: %v", err)
	}
	return version
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(
		"SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?", name).Scan(&count)
	if err != nil {
		t.Fatalf("querying sqlite_schema: %v", err)
	}
	return count == 1
}

func writeSequenceRow(t *testing.T, path, kind string, nextN int) {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("INSERT INTO sequence (kind, next_n) VALUES (?, ?)", kind, nextN); err != nil {
		t.Fatalf("inserting sequence row: %v", err)
	}
}

func readSequenceRow(t *testing.T, path, kind string) int {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	var nextN int
	if err := db.QueryRow("SELECT next_n FROM sequence WHERE kind = ?", kind).Scan(&nextN); err != nil {
		t.Fatalf("reading sequence row: %v", err)
	}
	return nextN
}

func setUserVersion(t *testing.T, path string, version int) {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(version)); err != nil {
		t.Fatalf("setting user_version: %v", err)
	}
}
