// Package store owns the SQLite database: where it lives, how it is opened,
// and the schema applied to it.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/scottlaird/roz/internal/schema"
)

// dirPerm applies to the directory Init creates. The database holds work
// notes, so it is not world-readable.
const dirPerm = 0o700

// Init prepares the database at path, creating its parent directory if
// needed, migrating it to the current schema, and seeding one sequence row
// per entity using the requested identifier prefixes.
//
// It is safe to re-run. An already-initialised database keeps its data and
// its prefixes; nothing is dropped or overwritten. A database ahead of this
// build is an error rather than something to guess at.
//
// Seeding is separate from migrating, so a run that migrates but fails to
// seed leaves a valid schema and seeds on the next attempt.
func Init(ctx context.Context, path string, requested map[Entity]string) (InitResult, error) {
	if err := validatePrefixes(requested); err != nil {
		return InitResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return InitResult{}, fmt.Errorf("creating database directory: %w", err)
	}

	db, err := Open(path)
	if err != nil {
		return InitResult{}, err
	}
	defer db.Close()

	from, to, err := migrate(ctx, db)
	if err != nil {
		return InitResult{}, err
	}
	result := InitResult{From: from, To: to}

	seeded, err := sequencesSeeded(db)
	if err != nil {
		return InitResult{}, err
	}
	if seeded {
		stored, err := LoadPrefixes(db)
		if err != nil {
			return InitResult{}, err
		}
		result.Prefixes = stored
		return result, nil
	}

	if err := seedInTransaction(ctx, db, requested); err != nil {
		return InitResult{}, err
	}
	result.Created = true
	result.Prefixes = requested
	return result, nil
}

// InitResult is what Init found and what it did, so a caller can report the
// database's state rather than infer it from a boolean.
type InitResult struct {
	// Created reports whether the sequences were seeded here, which is the
	// real signal that the database is new.
	Created bool

	// From and To are the schema versions either side of the run. They are
	// equal when there was nothing to apply, and From is zero for a database
	// that did not exist.
	From, To int

	// Prefixes are the ones now in force: for an existing database the stored
	// ones, not the requested ones, since prefixes are write-once.
	Prefixes map[Entity]string
}

func sequencesSeeded(db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sequence").Scan(&count); err != nil {
		return false, fmt.Errorf("reading the sequence table: %w", err)
	}
	return count > 0, nil
}

func seedInTransaction(ctx context.Context, db *sql.DB, prefixes map[Entity]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("seeding sequences: %w", err)
	}
	defer tx.Rollback()

	if err := seedSequences(tx, prefixes); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrNotInitialised means the database has not been through Init. Callers
// should test for it with errors.Is and point the user at `roz init` rather
// than reporting a missing table.
var ErrNotInitialised = errors.New("database is not initialised")

// OpenStore opens an initialised database and returns a Store over it.
//
// Unlike Open it never creates a file: a missing or unstamped database is
// ErrNotInitialised, so a mistyped --db path reports that rather than
// silently creating an empty database and failing later.
//
// The caller must Close the result.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", path, ErrNotInitialised)
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	db, err := Open(path)
	if err != nil {
		return nil, err
	}

	if err := checkSchemaUpToDate(ctx, db, path); err != nil {
		db.Close()
		return nil, err
	}

	// A verb naming a predicate this build lacks is refused here rather than
	// discovered later, when an action would quietly stop closing.
	if err := checkPredicates(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	st, err := New(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

// checkSchemaUpToDate refuses a database that has not run every migration
// this build carries, or that has run one this build does not have.
//
// It reads the record of what was applied rather than a version number, so a
// migration numbered below one already applied still shows up as pending
// instead of being passed over.
func checkSchemaUpToDate(ctx context.Context, db *sql.DB, path string) error {
	var exists int
	err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'applied_migration'").Scan(&exists)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if exists == 0 {
		// Either brand new, or old enough to predate the record. Either way
		// init is what sorts it out.
		if version, err := userVersion(db); err == nil && version > 0 {
			return fmt.Errorf("database %s predates the migration record; run `roz init` to bring it up to date", path)
		}
		return fmt.Errorf("%s: %w", path, ErrNotInitialised)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		return fmt.Errorf("%s: %w", path, ErrNotInitialised)
	}

	pending, err := PendingMigrations(ctx, db)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("database %s has not run %s; run `roz init` to migrate",
			path, MigrationNames(pending))
	}

	migrations, err := schema.Migrations()
	if err != nil {
		return err
	}
	known := make(map[int]bool, len(migrations))
	for _, m := range migrations {
		known[m.Version] = true
	}
	for version := range applied {
		if !known[version] {
			return fmt.Errorf("database %s has run migration %d, which this build does not have",
				path, version)
		}
	}
	return nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

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
	// LIKE is case-insensitive for ASCII by default, which is a surprise
	// waiting to happen and the reason a filter's startsWith could not be
	// pushed into a query: `title LIKE 'fix%'` matched "Fix the thing" where
	// CEL's startsWith did not, so the same expression meant two things.
	//
	// Nothing in roz wanted the insensitivity. The only LIKE in its own SQL is
	// migration 0028's assertion against sqlite_schema, whose pattern already
	// matches the stored DDL exactly, case included.
	//
	// A DSN parameter rather than an Exec, for the reason the other three are:
	// this is per-connection and database/sql pools connections, so a plain
	// PRAGMA would leave most of them without it — and half a pool answering
	// LIKE differently is worse than either answer.
	q.Add("_pragma", "case_sensitive_like(1)")
	return "file:" + path + "?" + q.Encode()
}

func userVersion(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	return version, nil
}
