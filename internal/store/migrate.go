package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/scottlaird/todo/internal/schema"
)

// migrate brings a database up to the latest schema version and reports where
// it started and finished.
//
// Each migration runs in its own transaction, with PRAGMA user_version set
// inside it, so a failure leaves the database at the last version that
// applied cleanly rather than part way through one. Migrations are applied in
// order and never re-applied.
//
// A database ahead of this build is an error: an older binary writing to a
// newer schema is how data gets quietly mangled.
func migrate(ctx context.Context, db *sql.DB) (from, to int, err error) {
	migrations, err := schema.Migrations()
	if err != nil {
		return 0, 0, err
	}
	if len(migrations) == 0 {
		return 0, 0, fmt.Errorf("no migrations are embedded")
	}
	latest := migrations[len(migrations)-1].Version

	current, err := userVersion(db)
	if err != nil {
		return 0, 0, err
	}
	if current > latest {
		return 0, 0, fmt.Errorf(
			"database is at schema version %d, but this build only knows %d", current, latest)
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return current, current, err
		}
	}
	return current, latest, nil
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

	// PRAGMA takes no bind parameters; the version comes from a file name
	// already parsed as an integer.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
		return fmt.Errorf("stamping version after %s: %w", m.Name, err)
	}
	return tx.Commit()
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
