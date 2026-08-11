package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/scottlaird/roz/internal/schema"
)

// nowStamp is when a migration ran. Migrations happen outside any Tx, so
// they do not have a unit of work's fixed timestamp to borrow.
func nowStamp() string { return time.Now().UTC().Format(timeFormat) }

// createAppliedMigration is the runner's own bookkeeping, so it cannot be a
// numbered migration itself. It is created before anything else runs and is
// harmless to repeat.
//
// schema.sql carries the same statement, and TestSchemaMatchesMigrations
// compares them, so the two cannot drift.
const createAppliedMigration = `
CREATE TABLE IF NOT EXISTS applied_migration (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at TEXT NOT NULL
) STRICT`

// migrate brings a database up to date and reports where it started and
// finished.
//
// Which migrations have run is read from applied_migration rather than
// inferred from a single cursor. PRAGMA user_version is a high-water mark, so
// a migration numbered below one already applied would be skipped and never
// noticed — which is what happens whenever two branches each add a migration
// and land in the other order. Recording each version applied removes the
// question.
//
// Each migration runs in its own transaction, with its bookkeeping row
// written inside it, so a failure leaves the database at the last migration
// that applied cleanly rather than part way through one.
//
// A database carrying a migration this build does not have is an error: an
// older binary writing to a newer schema is how data gets quietly mangled.
func migrate(ctx context.Context, db *sql.DB) (from, to int, err error) {
	migrations, err := schema.Migrations()
	if err != nil {
		return 0, 0, err
	}
	if len(migrations) == 0 {
		return 0, 0, fmt.Errorf("no migrations are embedded")
	}

	if err := ensureBookkeeping(ctx, db); err != nil {
		return 0, 0, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return 0, 0, err
	}
	from = highest(applied)

	known := make(map[int]bool, len(migrations))
	for _, m := range migrations {
		known[m.Version] = true
	}
	var unknown []int
	for version := range applied {
		if !known[version] {
			unknown = append(unknown, version)
		}
	}
	if len(unknown) > 0 {
		sort.Ints(unknown)
		return from, from, fmt.Errorf(
			"database has run migration(s) %v that this build does not have; it was written by a newer version",
			unknown)
	}

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return from, highest(applied), err
		}
		applied[m.Version] = true
	}
	return from, highest(applied), nil
}

// ensureBookkeeping creates the applied_migration table and, for a database
// that predates it, records what must already have run.
//
// The backfill reads PRAGMA user_version, which is all the old scheme left
// behind: a database at version N had run every migration up to N, since that
// was the only order the old runner could apply them in.
func ensureBookkeeping(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createAppliedMigration); err != nil {
		return fmt.Errorf("creating the migration record: %w", err)
	}

	var recorded int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM applied_migration").Scan(&recorded); err != nil {
		return fmt.Errorf("reading the migration record: %w", err)
	}
	if recorded > 0 {
		return nil
	}

	version, err := userVersion(db)
	if err != nil {
		return err
	}
	if version == 0 {
		return nil // a new database; nothing has run
	}

	migrations, err := schema.Migrations()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("recording earlier migrations: %w", err)
	}
	defer tx.Rollback()

	for _, m := range migrations {
		if m.Version > version {
			break
		}
		if err := recordApplied(ctx, tx, m, "backfilled from user_version"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM applied_migration")
	if err != nil {
		return nil, fmt.Errorf("reading the migration record: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("reading the migration record: %w", err)
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func highest(versions map[int]bool) int {
	var top int
	for version := range versions {
		if version > top {
			top = version
		}
	}
	return top
}

func applyMigration(ctx context.Context, db *sql.DB, m schema.Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("applying %s: %w", m.Name, err)
	}
	defer tx.Rollback()

	// Foreign keys cannot be turned off inside a transaction — the pragma is
	// silently ignored there — so a migration that rebuilds a table defers
	// the checks to commit instead, and they are verified below.
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return fmt.Errorf("applying %s: %w", m.Name, err)
	}
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("applying %s: %w", m.Name, err)
	}
	if err := checkForeignKeys(ctx, tx); err != nil {
		return fmt.Errorf("applying %s: %w", m.Name, err)
	}
	if err := recordApplied(ctx, tx, m, nowStamp()); err != nil {
		return err
	}

	// user_version is no longer the authority, but it is kept current: it is
	// what a sqlite3 shell shows, and it is cheap.
	//
	// PRAGMA takes no bind parameters; the version comes from a file name
	// already parsed as an integer.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
		return fmt.Errorf("stamping version after %s: %w", m.Name, err)
	}
	return tx.Commit()
}

func recordApplied(ctx context.Context, tx *sql.Tx, m schema.Migration, at string) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO applied_migration (version, name, applied_at) VALUES (?, ?, ?)",
		m.Version, m.Name, at)
	if err != nil {
		return fmt.Errorf("recording %s: %w", m.Name, err)
	}
	return nil
}

// checkForeignKeys reports any row a rebuilt table left pointing at nothing.
// Deferred enforcement would catch this at commit, but not say which row.
func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("checking foreign keys: %w", err)
	}
	defer rows.Close()

	var violations []string
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("checking foreign keys: %w", err)
		}
		violations = append(violations,
			fmt.Sprintf("%s row %d references a missing %s", table, rowid.Int64, parent))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("checking foreign keys: %w", err)
	}
	if len(violations) > 0 {
		return fmt.Errorf("foreign key violations: %v", violations)
	}
	return nil
}

// PendingMigrations returns the migrations a database has not run, in order.
//
// This is what tells a command to say "run roz init" rather than guessing
// from a version number, and it is what makes an out-of-order migration
// visible instead of silently skipped.
func PendingMigrations(ctx context.Context, db *sql.DB) ([]schema.Migration, error) {
	migrations, err := schema.Migrations()
	if err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}

	var pending []schema.Migration
	for _, m := range migrations {
		if !applied[m.Version] {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

// MigrationNames renders migrations for an error message.
func MigrationNames(migrations []schema.Migration) string {
	names := make([]string, len(migrations))
	for i, m := range migrations {
		names[i] = m.Name
	}
	return strings.Join(names, ", ")
}

// SchemaState identifies the set of migrations a database has run.
//
// It is not a version number and is not meaningful on its own. Comparing two
// readings of the same database is the whole of what it is for: a
// long-running process takes one at startup and watches for it to change,
// which is how it notices being migrated under.
//
// Applied as well as Highest, because migrations do not always arrive in
// order — two branches each adding one and landing the other way round is the
// case applied_migration exists for — so the top number alone can stand still
// while the set grows.
type SchemaState struct {
	// Applied is how many migrations have run.
	Applied int
	// Highest is the largest version among them.
	Highest int
}

func (s SchemaState) String() string {
	return fmt.Sprintf("migration %d (%d applied)", s.Highest, s.Applied)
}

// SchemaState reads which migrations the database has run.
//
// One row-count against a table with a row per migration, so it is cheap
// enough to ask on a timer.
func (s *Store) SchemaState(ctx context.Context) (SchemaState, error) {
	var state SchemaState
	err := s.db.QueryRowContext(ctx,
		"SELECT count(*), coalesce(max(version), 0) FROM applied_migration").
		Scan(&state.Applied, &state.Highest)
	if err != nil {
		return SchemaState{}, fmt.Errorf("reading the migration record: %w", err)
	}
	return state, nil
}

// LatestSchemaVersion is the version a fully migrated database reports.
func LatestSchemaVersion() (int, error) {
	migrations, err := schema.Migrations()
	if err != nil {
		return 0, err
	}
	if len(migrations) == 0 {
		return 0, fmt.Errorf("no migrations are embedded")
	}
	return migrations[len(migrations)-1].Version, nil
}
