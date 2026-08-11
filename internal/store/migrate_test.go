package store

import (
	"context"
	"database/sql"
	"fmt"
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

// normaliseSQL strips what does not change meaning: comments, line breaks,
// spacing around punctuation, and identifier quoting. What survives is close
// enough to compare, and still catches a differing CHECK or default.
//
// Quotes have to go because ALTER TABLE ... RENAME TO writes the new name
// quoted, so a rebuilt table is stored as CREATE TABLE "pr" while schema.sql
// says CREATE TABLE pr. Double quotes only ever delimit identifiers here;
// string literals in this schema use single quotes.
func normaliseSQL(ddl string) string {
	ddl = lineComment.ReplaceAllString(ddl, " ")
	ddl = strings.ReplaceAll(ddl, `"`, "")
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

// TestSeededPipelines compares the pipelines and their steps, for the same
// reason as the vocabulary below: they are rows, so the schema comparison
// cannot see them, and a pipeline whose steps drift from the migration would
// instantiate the wrong chain with nothing to say so.
func TestSeededPipelines(t *testing.T) {
	documented := pipelineRows(t, applyDocumentedSchema(t))
	migrated := pipelineRows(t, applyMigrations(t))

	if len(documented) == 0 {
		t.Fatal("schema.sql seeds no pipelines")
	}
	compareSeeded(t, "pipeline", documented, migrated)
}

// pipelineRows reads the pipelines as comparable text, steps included, keyed
// by name.
func pipelineRows(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	steps := map[string]string{}
	stepRows, err := db.Query("SELECT pipeline, verb FROM pipeline_step ORDER BY pipeline, position")
	if err != nil {
		t.Fatalf("reading pipeline steps: %v", err)
	}
	defer stepRows.Close()
	for stepRows.Next() {
		var name, verb string
		if err := stepRows.Scan(&name, &verb); err != nil {
			t.Fatalf("scanning a step: %v", err)
		}
		steps[name] += " " + verb
	}
	if err := stepRows.Err(); err != nil {
		t.Fatalf("reading pipeline steps: %v", err)
	}

	rows, err := db.Query("SELECT n, name, label, active, description FROM action_pipeline")
	if err != nil {
		t.Fatalf("reading pipelines: %v", err)
	}
	defer rows.Close()

	pipelines := map[string]string{}
	for rows.Next() {
		var n, active int
		var name, label, description string
		if err := rows.Scan(&n, &name, &label, &active, &description); err != nil {
			t.Fatalf("scanning a pipeline: %v", err)
		}
		pipelines[name] = fmt.Sprintf("%d|%s|%d|%s|%s", n, label, active, description, steps[name])
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading pipelines: %v", err)
	}
	return pipelines
}

// TestSeededConfig: the config row is seeded rather than created by init, so
// that no reader has to handle its absence. That only holds if both ways of
// building a database produce it, and produce the same one.
//
// The timestamps are left out because they are stamped from the clock as the
// migration runs, so the two databases legitimately disagree about them.
func TestSeededConfig(t *testing.T) {
	documented := configRows(t, applyDocumentedSchema(t))
	migrated := configRows(t, applyMigrations(t))

	if len(documented) != 1 {
		t.Fatalf("schema.sql seeds %d config rows, want exactly 1", len(documented))
	}
	compareSeeded(t, "config", documented, migrated)
}

func configRows(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	rows, err := db.Query("SELECT id, owner, jira_base_url, jira_prefixes FROM config")
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	defer rows.Close()

	seeded := map[string]string{}
	for rows.Next() {
		var id, owner, base, prefixes string
		if err := rows.Scan(&id, &owner, &base, &prefixes); err != nil {
			t.Fatalf("scanning config: %v", err)
		}
		seeded[id] = fmt.Sprintf("owner=%q base=%q prefixes=%s", owner, base, prefixes)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading config: %v", err)
	}
	return seeded
}

// compareSeeded reports seeded rows that differ, are missing, or are extra.
func compareSeeded(t *testing.T, what string, documented, migrated map[string]string) {
	t.Helper()

	if len(documented) != len(migrated) {
		t.Fatalf("schema.sql seeds %d %ss, the migrations seed %d",
			len(documented), what, len(migrated))
	}
	for key, want := range documented {
		got, ok := migrated[key]
		if !ok {
			t.Errorf("%s %q is in schema.sql but no migration seeds it", what, key)
			continue
		}
		if got != want {
			t.Errorf("%s %q differs.\nschema.sql:  %s\nmigrations:  %s", what, key, want, got)
		}
	}
	for key := range migrated {
		if _, ok := documented[key]; !ok {
			t.Errorf("%s %q is seeded by a migration but missing from schema.sql", what, key)
		}
	}
}

// TestSeededVocabulary compares the verb rows, which sqlite_schema does not
// carry and TestSchemaMatchesMigrations therefore cannot see.
//
// The vocabulary is data, but it is data schema.sql describes, and content in
// that file is only worth having if something checks it. Without this, the
// verbs could drift from the migration and nothing would say so.
func TestSeededVocabulary(t *testing.T) {
	documented := verbRows(t, applyDocumentedSchema(t))
	migrated := verbRows(t, applyMigrations(t))

	if len(documented) == 0 {
		t.Fatal("schema.sql seeds no verbs")
	}
	compareSeeded(t, "verb", documented, migrated)
}

// verbRows reads the vocabulary as comparable text, keyed by verb.
func verbRows(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	rows, err := db.Query(`SELECT verb, label, closes, coalesce(predicate_key, ''),
	                              rank_class, requires_pr, starts_pipeline, active, description
	                       FROM actionverb`)
	if err != nil {
		t.Fatalf("reading the vocabulary: %v", err)
	}
	defer rows.Close()

	verbs := map[string]string{}
	for rows.Next() {
		var verb, label, closes, key, rank, description string
		var requiresPR, startsPipeline, active int
		if err := rows.Scan(&verb, &label, &closes, &key, &rank,
			&requiresPR, &startsPipeline, &active, &description); err != nil {
			t.Fatalf("scanning a verb: %v", err)
		}
		verbs[verb] = fmt.Sprintf("%s|%s|%s|%s|%d|%d|%d|%s",
			label, closes, key, rank, requiresPR, startsPipeline, active, description)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the vocabulary: %v", err)
	}
	return verbs
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
	// A migration from a newer build, recorded as having run here.
	_, err = db.Exec("INSERT INTO applied_migration (version, name, applied_at) VALUES (?, ?, ?)",
		latest+1, "9999_from_the_future.sql", "2027-01-01T00:00:00.000Z")
	if err != nil {
		t.Fatalf("recording the future migration: %v", err)
	}

	_, _, err = migrate(ctx, db)
	if err == nil {
		t.Fatal("migrate() on a newer database returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "does not have") {
		t.Errorf("error = %v, want it to say the build is behind", err)
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

// TestRebuildMigrationPreservesData runs the first real table rebuild against
// a database that already holds pull requests.
//
// Rebuilding is SQLite's only way to add a foreign key, and it is the step
// most likely to lose rows quietly, so this stops at version 1, puts data in,
// and then migrates the rest of the way.
func TestRebuildMigrationPreservesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "todo.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	migrations, err := schema.Migrations()
	if err != nil {
		t.Fatalf("Migrations() returned error: %v", err)
	}
	if len(migrations) < 2 {
		t.Skip("no rebuild migration yet")
	}
	if err := ensureBookkeeping(ctx, db); err != nil {
		t.Fatalf("ensureBookkeeping() returned error: %v", err)
	}
	if err := applyMigration(ctx, db, migrations[0]); err != nil {
		t.Fatalf("applying %s: %v", migrations[0].Name, err)
	}

	// A base pull request and one stacked on it, so the self-reference is
	// exercised as well as the row copy.
	const at = "2026-08-09T12:00:00.000Z"
	for _, insert := range []string{
		`INSERT INTO pr (id, repo, number, title, tracked_since)
		 VALUES ('owner/repo#1', 'owner/repo', 1, 'base', '` + at + `')`,
		`INSERT INTO pr (id, repo, number, title, stacked_on, tracked_since)
		 VALUES ('owner/repo#2', 'owner/repo', 2, 'stacked', 'owner/repo#1', '` + at + `')`,
	} {
		if _, err := db.Exec(insert); err != nil {
			t.Fatalf("seeding a pull request: %v", err)
		}
	}

	if _, _, err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate() returned error: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM pr").Scan(&count); err != nil {
		t.Fatalf("counting pull requests: %v", err)
	}
	if count != 2 {
		t.Errorf("%d pull requests survived the rebuild, want 2", count)
	}

	var stackedOn, title string
	err = db.QueryRow("SELECT stacked_on, title FROM pr WHERE id = 'owner/repo#2'").Scan(&stackedOn, &title)
	if err != nil {
		t.Fatalf("reading the stacked pull request: %v", err)
	}
	if stackedOn != "owner/repo#1" || title != "stacked" {
		t.Errorf("stacked pull request = %q / %q, want owner/repo#1 / stacked", stackedOn, title)
	}

	// The repository the pull requests named was backfilled, or the new
	// foreign key would have had nothing to point at.
	var owner, name string
	if err := db.QueryRow("SELECT owner, name FROM github_repo WHERE id = 'owner/repo'").Scan(&owner, &name); err != nil {
		t.Fatalf("the repository was not backfilled: %v", err)
	}
	if owner != "owner" || name != "repo" {
		t.Errorf("backfilled repository = %q / %q, want owner / repo", owner, name)
	}

	// And the constraint is live afterwards.
	_, err = db.Exec(`INSERT INTO pr (id, repo, number, tracked_since)
	                  VALUES ('other/repo#1', 'other/repo', 1, '` + at + `')`)
	if err == nil {
		t.Error("a pull request in an untracked repository was accepted after the rebuild")
	}
}

// TestRebuildMigrationRejectsUnsplittableRepo covers the case the backfill
// deliberately does not guess at: a repository name with no owner cannot
// become an owner/name key, so the migration fails rather than inventing one.
func TestRebuildMigrationRejectsUnsplittableRepo(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "todo.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	migrations, err := schema.Migrations()
	if err != nil {
		t.Fatalf("Migrations() returned error: %v", err)
	}
	if len(migrations) < 2 {
		t.Skip("no rebuild migration yet")
	}
	if err := ensureBookkeeping(ctx, db); err != nil {
		t.Fatalf("ensureBookkeeping() returned error: %v", err)
	}
	if err := applyMigration(ctx, db, migrations[0]); err != nil {
		t.Fatalf("applying %s: %v", migrations[0].Name, err)
	}

	_, err = db.Exec(`INSERT INTO pr (id, repo, number, tracked_since)
	                  VALUES ('bare#1', 'bare', 1, '2026-08-09T12:00:00.000Z')`)
	if err != nil {
		t.Fatalf("seeding a bare-named pull request: %v", err)
	}

	if err := applyMigration(ctx, db, migrations[1]); err == nil {
		t.Fatal("the migration accepted a repository it could not split, want an error")
	}

	// And it left the database where it was rather than half rebuilt.
	if got, _ := userVersion(db); got != migrations[0].Version {
		t.Errorf("user_version = %d after a failed migration, want %d", got, migrations[0].Version)
	}
}

// TestOutOfOrderMigrationIsStillApplied is the reason the record exists.
//
// Two branches each adding a migration can land in the other order, leaving a
// migration numbered below one already applied. Under a single cursor it
// would be passed over and never noticed; the record makes it pending.
func TestOutOfOrderMigrationIsStillApplied(t *testing.T) {
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

	// A migration numbered below the newest, as though it had been written on
	// a branch that landed second.
	late := schema.Migration{
		Version: to + 1,
		Name:    "late.sql",
		SQL:     "ALTER TABLE project ADD COLUMN late_arrival TEXT;",
	}
	if err := applyMigration(ctx, db, late); err != nil {
		t.Fatalf("applying the later migration: %v", err)
	}

	overtaken := schema.Migration{
		Version: to, // already passed by user_version, but never applied
		Name:    "overtaken.sql",
		SQL:     "ALTER TABLE project ADD COLUMN overtaken TEXT;",
	}
	if err := db.QueryRow("SELECT 1 FROM applied_migration WHERE version = ?", overtaken.Version).
		Scan(new(int)); err != nil {
		t.Fatalf("the base migration is not recorded: %v", err)
	}

	// The record says it ran, so it is not pending. Remove the row to stand
	// in for a migration that never ran despite a higher number existing.
	if _, err := db.Exec("DELETE FROM applied_migration WHERE version = ?", overtaken.Version); err != nil {
		t.Fatalf("clearing the record: %v", err)
	}

	pending, err := PendingMigrations(ctx, db)
	if err != nil {
		t.Fatalf("PendingMigrations() returned error: %v", err)
	}
	var found bool
	for _, m := range pending {
		if m.Version == overtaken.Version {
			found = true
		}
	}
	if !found {
		t.Errorf("a migration below the highest applied is not pending; it would be skipped")
	}
}

// TestBackfillFromUserVersion covers a database written before the record
// existed: what it already ran is inferred from the cursor it did keep.
func TestBackfillFromUserVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "todo.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	migrations, err := schema.Migrations()
	if err != nil {
		t.Fatalf("Migrations() returned error: %v", err)
	}

	// Apply the first two the way the old runner did: the SQL and the cursor,
	// and no record.
	for _, m := range migrations[:2] {
		if _, err := db.Exec(m.SQL); err != nil {
			t.Fatalf("applying %s: %v", m.Name, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(migrations[1].Version)); err != nil {
		t.Fatalf("setting user_version: %v", err)
	}

	if _, _, err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate() on a pre-record database returned error: %v", err)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		t.Fatalf("appliedVersions() returned error: %v", err)
	}
	for _, m := range migrations {
		if !applied[m.Version] {
			t.Errorf("%s is not recorded as applied", m.Name)
		}
	}

	// The backfilled rows say where they came from, so the record does not
	// pretend to know when they ran.
	var note string
	if err := db.QueryRow("SELECT applied_at FROM applied_migration WHERE version = ?",
		migrations[0].Version).Scan(&note); err != nil {
		t.Fatalf("reading the backfilled row: %v", err)
	}
	if !strings.Contains(note, "backfilled") {
		t.Errorf("backfilled row says %q, want it marked as such", note)
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

// TestSchemaState: a long-running process compares two readings, so what
// matters is that a fresh database reports what this build carries and that
// the value moves when the record does.
func TestSchemaState(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	migrations, err := schema.Migrations()
	if err != nil {
		t.Fatalf("Migrations() returned error: %v", err)
	}
	latest, err := LatestSchemaVersion()
	if err != nil {
		t.Fatalf("LatestSchemaVersion() returned error: %v", err)
	}

	state, err := st.SchemaState(ctx)
	if err != nil {
		t.Fatalf("SchemaState() returned error: %v", err)
	}
	if state.Applied != len(migrations) {
		t.Errorf("applied = %d, want %d", state.Applied, len(migrations))
	}
	if state.Highest != latest {
		t.Errorf("highest = %d, want %d", state.Highest, latest)
	}

	// Out of order on purpose: a migration numbered below one already applied
	// is the case applied_migration exists for, and the top number does not
	// move for it.
	if _, err := st.db.ExecContext(ctx,
		"INSERT INTO applied_migration (version, name, applied_at) VALUES (?, '0000_earlier.sql', '')",
		0); err != nil {
		t.Fatalf("recording a migration: %v", err)
	}

	moved, err := st.SchemaState(ctx)
	if err != nil {
		t.Fatalf("SchemaState() returned error: %v", err)
	}
	if moved == state {
		t.Errorf("state = %s unchanged, want it to move when the record does", moved)
	}
	if moved.Highest != state.Highest {
		t.Errorf("highest = %d, want it unmoved at %d: this is why Applied is counted too",
			moved.Highest, state.Highest)
	}
}
