package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/schema"
)

// newDBPath returns a path inside a fresh temp dir, with a missing parent
// directory so Init has to create one.
func newDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sub", "roz.db")
}

func TestInitCreatesDatabase(t *testing.T) {
	path := newDBPath(t)

	result, err := Init(path, testPrefixes())
	if err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	if want := true; result.Created != want {
		t.Errorf("Init() created = %v, want %v", result.Created, want)
	}
	if result.From != 0 || result.To == 0 {
		t.Errorf("Init() schema %d → %d, want 0 → the current version", result.From, result.To)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	latest, err := LatestSchemaVersion()
	if err != nil {
		t.Fatalf("LatestSchemaVersion() returned error: %v", err)
	}
	if got := userVersionOf(t, db); got != latest {
		t.Errorf("user_version = %d, want the latest migration %d", got, latest)
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

	if _, err := Init(path, testPrefixes()); err != nil {
		t.Fatalf("first Init() returned error: %v", err)
	}
	setNextN(t, path, EntityAction, 42)

	result, err := Init(path, testPrefixes())
	if err != nil {
		t.Fatalf("second Init() returned error: %v", err)
	}
	if want := false; result.Created != want {
		t.Errorf("second Init() created = %v, want %v", result.Created, want)
	}
	if result.From != result.To {
		t.Errorf("re-init migrated %d → %d, want nothing to apply", result.From, result.To)
	}

	// A reset counter would hand out an identifier that has already been used.
	if got, want := nextN(t, path, EntityAction), 42; got != want {
		t.Errorf("next_n for %s = %d, want %d", EntityAction, got, want)
	}
}

func TestInitRejectsUnknownSchemaVersion(t *testing.T) {
	path := newDBPath(t)

	if _, err := Init(path, testPrefixes()); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	latest, err := LatestSchemaVersion()
	if err != nil {
		t.Fatalf("LatestSchemaVersion() returned error: %v", err)
	}
	recordFutureMigration(t, path, latest+1)

	if _, err := Init(path, testPrefixes()); err == nil {
		t.Error("Init() on a newer schema version returned nil, want an error")
	}
}

func TestInitCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "roz.db")

	if _, err := Init(path, testPrefixes()); err != nil {
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
	if _, err := Init(path, testPrefixes()); err != nil {
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
	if _, err := Init(path, testPrefixes()); err != nil {
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

	if _, err := Init(path, testPrefixes()); err == nil {
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

func testPrefixes() map[Entity]string {
	return map[Entity]string{EntityProject: "SL", EntityAction: "NA"}
}

func setNextN(t *testing.T, path string, entity Entity, n int) {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("UPDATE sequence SET next_n = ? WHERE entity = ?", n, string(entity)); err != nil {
		t.Fatalf("updating next_n: %v", err)
	}
}

func nextN(t *testing.T, path string, entity Entity) int {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow("SELECT next_n FROM sequence WHERE entity = ?", string(entity)).Scan(&n); err != nil {
		t.Fatalf("reading next_n: %v", err)
	}
	return n
}

// recordFutureMigration marks a migration this build does not have as
// applied, which is how a database written by a newer version looks.
func recordFutureMigration(t *testing.T, path string, version int) {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	_, err = db.Exec("INSERT INTO applied_migration (version, name, applied_at) VALUES (?, ?, ?)",
		version, "9999_from_the_future.sql", "2027-01-01T00:00:00.000Z")
	if err != nil {
		t.Fatalf("recording the future migration: %v", err)
	}
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

// TestInitReportsAMigration: a re-run that moves the schema has to report the
// versions it moved between, since saying so is the whole reason to re-run.
func TestInitReportsAMigration(t *testing.T) {
	ctx := context.Background()
	path := newDBPath(t)

	migrations, err := schema.Migrations()
	if err != nil {
		t.Fatalf("Migrations() returned error: %v", err)
	}
	if len(migrations) < 2 {
		t.Skip("needs at least two migrations to stop short of the newest")
	}
	behind := migrations[:len(migrations)-1]

	// Build the database the way a previous build would have left it: every
	// migration but the last, and seeded, so this is not a new database.
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	if err := ensureBookkeeping(ctx, db); err != nil {
		t.Fatalf("ensureBookkeeping() returned error: %v", err)
	}
	for _, m := range behind {
		if err := applyMigration(ctx, db, m); err != nil {
			t.Fatalf("applying %s: %v", m.Name, err)
		}
	}
	if err := seedInTransaction(ctx, db, testPrefixes()); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	db.Close()

	result, err := Init(path, testPrefixes())
	if err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	if result.Created {
		t.Error("Init() created = true on a seeded database, want false")
	}
	if got, want := result.From, behind[len(behind)-1].Version; got != want {
		t.Errorf("Init() from = %d, want %d", got, want)
	}
	if got, want := result.To, migrations[len(migrations)-1].Version; got != want {
		t.Errorf("Init() to = %d, want %d", got, want)
	}
}
