package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/scottlaird/todo/internal/schema"
)

// objectSQL is every table, index and trigger a database holds, keyed by
// name and normalised so formatting differences do not register as drift.
func objectSQL(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_schema
	                       WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("reading sqlite_schema: %v", err)
	}
	defer rows.Close()

	objects := make(map[string]string)
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			t.Fatalf("scanning sqlite_schema: %v", err)
		}
		objects[kind+" "+name] = normaliseSQL(ddl)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading sqlite_schema: %v", err)
	}
	return objects
}

var (
	lineComment = regexp.MustCompile(`--[^\n]*`)
	runOfSpace  = regexp.MustCompile(`\s+`)
	spaceAround = regexp.MustCompile(`\s*([(),])\s*`)
)

// normaliseSQL strips what does not change meaning: comments, line breaks and
// spacing around punctuation. What survives is close enough to compare, and
// still catches a differing CHECK constraint or default.
func normaliseSQL(ddl string) string {
	ddl = lineComment.ReplaceAllString(ddl, " ")
	ddl = runOfSpace.ReplaceAllString(ddl, " ")
	ddl = spaceAround.ReplaceAllString(ddl, "$1")
	return strings.TrimSpace(ddl)
}

// applyDocumentedSchema builds a database straight from schema.sql, the file
// nothing else executes.
func applyDocumentedSchema(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "documented.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(schema.SQL); err != nil {
		t.Fatalf("schema.sql does not apply: %v", err)
	}
	return db
}

// applyMigrations builds a database the way the store does.
func applyMigrations(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "migrated.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, _, err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate() returned error: %v", err)
	}
	return db
}

// TestSchemaMatchesMigrations is what lets schema.sql stay hand-written. It
// is documentation, so nothing would otherwise notice it going stale.
func TestSchemaMatchesMigrations(t *testing.T) {
	documented := objectSQL(t, applyDocumentedSchema(t))
	migrated := objectSQL(t, applyMigrations(t))

	for name, want := range documented {
		got, ok := migrated[name]
		if !ok {
			t.Errorf("%s is in schema.sql but no migration creates it", name)
			continue
		}
		if got != want {
			t.Errorf("%s differs.\nschema.sql:  %s\nmigrations:  %s", name, want, got)
		}
	}
	for name := range migrated {
		if _, ok := documented[name]; !ok {
			t.Errorf("%s is created by a migration but missing from schema.sql", name)
		}
	}
}

func TestMigrationsAreWellFormed(t *testing.T) {
	migrations, err := schema.Migrations()
	if err != nil {
		t.Fatalf("Migrations() returned error: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations are embedded")
	}

	for i, m := range migrations {
		if m.Version < 1 {
			t.Errorf("%s has version %d; versions start at 1", m.Name, m.Version)
		}
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Errorf("%s (%d) does not come after %s (%d)",
				m.Name, m.Version, migrations[i-1].Name, migrations[i-1].Version)
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Errorf("%s is empty", m.Name)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "todo.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	from, to, err := migrate(ctx, db)
	if err != nil {
		t.Fatalf("first migrate() returned error: %v", err)
	}
	if from != 0 {
		t.Errorf("first migrate() started at %d, want 0", from)
	}

	again, stillTo, err := migrate(ctx, db)
	if err != nil {
		t.Fatalf("second migrate() returned error: %v", err)
	}
	if again != to || stillTo != to {
		t.Errorf("second migrate() went %d → %d, want %d → %d", again, stillTo, to, to)
	}
}

func TestMigrateRefusesANewerDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "todo.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	if _, _, err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate() returned error: %v", err)
	}

	latest, err := LatestSchemaVersion()
	if err != nil {
		t.Fatalf("LatestSchemaVersion() returned error: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(latest+1)); err != nil {
		t.Fatalf("setting user_version: %v", err)
	}

	if _, _, err := migrate(ctx, db); err == nil {
		t.Error("migrate() on a newer database returned nil, want an error")
	}
}

// TestApplyMigrationRunsArbitrarySQL exercises the path a second migration
// will take, which the single embedded migration cannot reach on its own.
func TestApplyMigrationRunsArbitrarySQL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "todo.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	if _, to, err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate() returned error: %v", err)
	} else {
		next := schema.Migration{
			Version: to + 1,
			Name:    "test_add_column.sql",
			SQL:     "ALTER TABLE project ADD COLUMN scratch TEXT;",
		}
		if err := applyMigration(ctx, db, next); err != nil {
			t.Fatalf("applyMigration() returned error: %v", err)
		}
		if got, _ := userVersion(db); got != next.Version {
			t.Errorf("user_version = %d, want %d", got, next.Version)
		}
	}

	if _, err := db.Exec("SELECT scratch FROM project"); err != nil {
		t.Errorf("the added column is not queryable: %v", err)
	}
}

// TestFailedMigrationLeavesVersionAlone is the property that makes a partial
// migration impossible: the version moves inside the same transaction.
func TestFailedMigrationLeavesVersionAlone(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "todo.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	_, to, err := migrate(ctx, db)
	if err != nil {
		t.Fatalf("migrate() returned error: %v", err)
	}

	// The first statement succeeds and the second does not.
	broken := schema.Migration{
		Version: to + 1,
		Name:    "test_broken.sql",
		SQL: `ALTER TABLE project ADD COLUMN scratch TEXT;
		      ALTER TABLE nonexistent ADD COLUMN oops TEXT;`,
	}
	if err := applyMigration(ctx, db, broken); err == nil {
		t.Fatal("applyMigration() on broken SQL returned nil, want an error")
	}

	if got, _ := userVersion(db); got != to {
		t.Errorf("user_version = %d after a failed migration, want %d", got, to)
	}
	if _, err := db.Exec("SELECT scratch FROM project"); err == nil {
		t.Error("the first statement survived a failed migration, want the whole thing rolled back")
	}
}

// TestMigrateStampsEachVersion checks the version is set inside the same
// transaction as the migration, so a database is never at a version whose
// migration did not fully apply.
func TestMigrateStampsEachVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "todo.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	if _, to, err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate() returned error: %v", err)
	} else {
		got, err := userVersion(db)
		if err != nil {
			t.Fatalf("userVersion() returned error: %v", err)
		}
		if got != to {
			t.Errorf("user_version = %d after migrating to %d", got, to)
		}
	}
}
