// Package store owns the SQLite database: where it lives, how it is opened,
// and the schema applied to it.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/scottlaird/todo/internal/schema"
)

// schemaVersion is stamped into PRAGMA user_version when the schema is
// applied, and checked on every Init.
//
// Schema changes: there is no migration machinery, and none is needed while
// there is only one version. When the DDL first changes, this constant is the
// hook — bump it, and have Init step a database forward from whatever version
// it holds. Until then Init refuses any version it does not recognise rather
// than guessing, so an older binary cannot quietly write to a newer database.
const schemaVersion = 1

// dirPerm applies to the directory Init creates. The database holds work
// notes, so it is not world-readable.
const dirPerm = 0o700

// Init prepares the database at path, creating its parent directory if
// needed, and reports whether it created the schema.
//
// It is safe to re-run. An already-initialised database is left untouched and
// Init returns false; nothing is dropped, overwritten or migrated. A database
// stamped with an unrecognised schema version is an error, not something to
// repair.
//
// Applying the schema is atomic: on failure the file is left without a
// user_version, so a later Init retries from the start.
func Init(path string) (created bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return false, fmt.Errorf("creating database directory: %w", err)
	}

	db, err := Open(path)
	if err != nil {
		return false, err
	}
	defer db.Close()

	version, err := userVersion(db)
	if err != nil {
		return false, err
	}
	switch version {
	case schemaVersion:
		return false, nil
	case 0:
		if err := applySchema(db); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf(
			"database %s has schema version %d, this build understands %d",
			path, version, schemaVersion)
	}
}

// Open opens the database at path, creating an empty file if it does not
// exist. It does not apply or check the schema; use Init for that.
//
// The caller must Close the result.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return db, nil
}

// dsn builds the connection string. The pragmas travel in the DSN because
// foreign_keys and busy_timeout are per-connection and database/sql pools
// connections: setting them with a plain Exec would leave every other
// connection in the pool without them.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	return "file:" + path + "?" + q.Encode()
}

func userVersion(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	return version, nil
}

func applySchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(schema.SQL); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	// PRAGMA does not take bind parameters; schemaVersion is a constant.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("stamping schema version: %w", err)
	}
	return tx.Commit()
}
