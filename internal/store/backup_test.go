package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupPathIsTimestampedBesideTheDatabase(t *testing.T) {
	at := time.Date(2026, 8, 11, 6, 42, 31, 0, time.UTC)

	got := BackupPath("/home/scott/.local/share/roz/roz.db", at)
	want := "/home/scott/.local/share/roz/backups/roz-20260811T064231Z.db"
	if got != want {
		t.Errorf("BackupPath() = %q, want %q", got, want)
	}
}

// TestBackupPathIsUTC: a local-time stamp would sort wrongly across a
// daylight-saving change, and repeat an hour outright.
func TestBackupPathIsUTC(t *testing.T) {
	zone := time.FixedZone("UTC+10", 10*60*60)
	at := time.Date(2026, 8, 11, 16, 42, 31, 0, zone)

	if got, want := BackupPath("/db/roz.db", at), "/db/backups/roz-20260811T064231Z.db"; got != want {
		t.Errorf("BackupPath() = %q, want %q", got, want)
	}
}

func TestBackupIsAReadableDatabase(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	want := addProject(t, st, "worth keeping")

	dest := filepath.Join(t.TempDir(), "backups", "roz.db")
	if err := st.Backup(ctx, dest); err != nil {
		t.Fatalf("Backup() returned error: %v", err)
	}

	// Through OpenStore rather than Open: a backup that is not a database
	// this build would agree to open is not a backup.
	restored, err := OpenStore(context.Background(), dest)
	if err != nil {
		t.Fatalf("opening the backup: %v", err)
	}
	defer restored.Close()

	projects, err := restored.ListProjects(ctx, ProjectFilter{})
	if err != nil {
		t.Fatalf("reading the project back: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("backup holds %d projects, want 1", len(projects))
	}
	if got := projects[0].Title; got != want.Title {
		t.Errorf("backup has project title %q, want %q", got, want.Title)
	}
}

// newStoreAt is newStore, but says where the database is: the restore tests
// need to name the file, not just talk to it.
func newStoreAt(t *testing.T) (*Store, string) {
	t.Helper()
	path := newDBPath(t)
	if _, err := Init(context.Background(), path, testPrefixes()); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := New(db)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	return st, path
}

// TestBackupRefusesToOverwrite pins the behaviour the timestamped default
// exists to work with. Losing yesterday's backup to today's is not a thing
// this should do quietly, so nothing here adds a --force.
func TestBackupRefusesToOverwrite(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	dest := filepath.Join(t.TempDir(), "roz.db")

	if err := st.Backup(ctx, dest); err != nil {
		t.Fatalf("first Backup() returned error: %v", err)
	}
	err := st.Backup(ctx, dest)
	if err == nil {
		t.Fatal("second Backup() to the same path returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second Backup() error = %v, want it to say the file is there", err)
	}
}

func TestBackupCreatesItsDirectory(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// VACUUM INTO will not make one, so Backup has to.
	dest := filepath.Join(t.TempDir(), "backups", "roz.db")
	if err := st.Backup(ctx, dest); err != nil {
		t.Fatalf("Backup() returned error: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("backup not written: %v", err)
	}
}

// backupOf returns a backup of a database holding one project, and the title
// it holds, which is how the restore tests tell the two databases apart.
func backupOf(t *testing.T, title string) (path, wantTitle string) {
	t.Helper()
	st := newStore(t)
	addProject(t, st, title)

	path = filepath.Join(t.TempDir(), "backup.db")
	if err := st.Backup(context.Background(), path); err != nil {
		t.Fatalf("Backup() returned error: %v", err)
	}
	return path, title
}

// titleOf reads the first project's title out of a database, to say which of
// the two a file is.
func titleOf(t *testing.T, path string) string {
	t.Helper()
	st, err := OpenStore(context.Background(), path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer st.Close()

	projects, err := st.ListProjects(context.Background(), ProjectFilter{})
	if err != nil {
		t.Fatalf("reading projects from %s: %v", path, err)
	}
	if len(projects) == 0 {
		t.Fatalf("%s holds no projects", path)
	}
	return projects[0].Title
}

func TestRestoreIntoAnEmptyPath(t *testing.T) {
	src, want := backupOf(t, "the backed up one")
	dst := filepath.Join(t.TempDir(), "sub", "roz.db")

	movedAside, err := Restore(context.Background(), src, dst, false)
	if err != nil {
		t.Fatalf("Restore() returned error: %v", err)
	}
	if movedAside != "" {
		t.Errorf("Restore() moved %q aside, want nothing: there was no database there", movedAside)
	}
	if got := titleOf(t, dst); got != want {
		t.Errorf("restored database holds %q, want %q", got, want)
	}
}

func TestRestoreRefusesAnExistingDatabase(t *testing.T) {
	src, _ := backupOf(t, "the backed up one")
	live, dst := newStoreAt(t)
	addProject(t, live, "the live one")

	_, err := Restore(context.Background(), src, dst, false)
	if err == nil {
		t.Fatal("Restore() over an existing database returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "--replace") {
		t.Errorf("Restore() error = %v, want it to name the flag that would allow this", err)
	}
	if got, want := titleOf(t, dst), "the live one"; got != want {
		t.Errorf("the database was changed anyway: holds %q, want %q", got, want)
	}
}

func TestRestoreReplacesAndKeepsTheOldOne(t *testing.T) {
	src, want := backupOf(t, "the backed up one")
	live, dst := newStoreAt(t)
	addProject(t, live, "the live one")
	live.Close()

	movedAside, err := Restore(context.Background(), src, dst, true)
	if err != nil {
		t.Fatalf("Restore() returned error: %v", err)
	}
	if got := titleOf(t, dst); got != want {
		t.Errorf("restored database holds %q, want %q", got, want)
	}

	// The point of moving it aside rather than deleting: a restore of the
	// wrong file has to be undoable.
	if movedAside == "" {
		t.Fatal("Restore() reported nothing moved aside, want the old database kept")
	}
	if got, want := titleOf(t, movedAside), "the live one"; got != want {
		t.Errorf("the kept database holds %q, want %q", got, want)
	}
}

// TestRestoreClearsStaleSidecars is the one that matters. A -wal left from the
// database that was replaced describes different content, and SQLite will
// apply it to whatever now sits at that path.
//
// The sidecars are written by hand rather than left over from the store above,
// because a clean Close checkpoints them away — the case this guards against
// is the process that never got to close cleanly.
func TestRestoreClearsStaleSidecars(t *testing.T) {
	src, want := backupOf(t, "the backed up one")
	live, dst := newStoreAt(t)
	addProject(t, live, "the live one")
	live.Close()

	for _, suffix := range sidecars {
		if err := os.WriteFile(dst+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatalf("planting a stale %s: %v", suffix, err)
		}
	}

	if _, err := Restore(context.Background(), src, dst, true); err != nil {
		t.Fatalf("Restore() returned error: %v", err)
	}
	for _, suffix := range sidecars {
		if _, err := os.Stat(dst + suffix); !os.IsNotExist(err) {
			t.Errorf("%s survived the restore (stat err = %v), want it gone", dst+suffix, err)
		}
	}
	if got := titleOf(t, dst); got != want {
		t.Errorf("restored database holds %q, want %q", got, want)
	}
}

func TestRestoreRejectsSomethingThatIsNotADatabase(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(src, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "roz.db")

	if _, err := Restore(context.Background(), src, dst, false); err == nil {
		t.Fatal("Restore() from a text file returned nil, want an error")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a database was created at %s anyway (stat err = %v)", dst, err)
	}
}
