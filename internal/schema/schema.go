// Package schema holds the SQLite DDL for the roz database.
//
// The database is built by the numbered files in migrations/, which are the
// only thing ever executed. schema.sql is a hand-written description of the
// current shape, kept for reading; the store's tests build a database each
// way and compare them, so the two cannot drift.
package schema

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// SQL is the documented current schema. Nothing executes it but the test that
// checks it still matches the migrations.
//
//go:embed schema.sql
var SQL string

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one numbered step, applied whole and in its own transaction.
type Migration struct {
	// Version is the number the file name begins with, and the value
	// PRAGMA user_version takes once it has been applied.
	Version int
	// Name is the file name, for error messages.
	Name string
	SQL  string
}

// Migrations returns every migration in ascending version order.
//
// Version numbers must be unique and start at 1. A gap is allowed — a
// migration abandoned before merging leaves one — but a duplicate is an
// error, since two files would claim the same user_version.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading migrations: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := versionOf(entry.Name())
		if err != nil {
			return nil, err
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s are both version %d", other, entry.Name(), version)
		}
		seen[version] = entry.Name()

		body, err := fs.ReadFile(migrationFS, path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, Migration{
			Version: version,
			Name:    entry.Name(),
			SQL:     string(body),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

// versionOf reads the leading number from a name like 0002_github_repo.sql.
func versionOf(name string) (int, error) {
	digits, _, found := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
	if !found {
		return 0, fmt.Errorf("migration %s is not named <version>_<description>.sql", name)
	}
	version, err := strconv.Atoi(digits)
	if err != nil {
		return 0, fmt.Errorf("migration %s does not begin with a version number", name)
	}
	if version < 1 {
		return 0, fmt.Errorf("migration %s has version %d; versions start at 1", name, version)
	}
	return version, nil
}
