package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// backupDir is the subdirectory backups land in, beside the database.
const backupDir = "backups"

// backupStamp orders names the way the backups happened: UTC, to the second,
// and sortable as text.
const backupStamp = "20060102T150405Z"

// sidecars are the files SQLite keeps beside a database in WAL mode. They
// belong to the database they were written for, so a file that replaces one
// has to leave none of them behind.
var sidecars = []string{"-wal", "-shm"}

// Backup writes a consistent copy of the database to path.
//
// VACUUM INTO rather than a file copy: in WAL mode the committed data is
// split between the database file and the -wal beside it, so copying the file
// alone can catch a torn state. VACUUM INTO reads through one transaction and
// writes a single file with nothing outstanding — safe against a database
// being written to at the same time.
//
// It will not overwrite. SQLite refuses that itself and nothing here softens
// it: a backup that can silently replace another is not one.
func (s *Store) Backup(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("creating the backup directory: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("backing up to %s: %w", path, err)
	}
	return nil
}

// BackupPath is where a backup goes when nobody said: a timestamped file in
// backups/ beside the database.
//
// Timestamped because VACUUM INTO refuses to overwrite, so any fixed name
// would work exactly once and then start failing.
func BackupPath(dbPath string, at time.Time) string {
	base := strings.TrimSuffix(filepath.Base(dbPath), filepath.Ext(dbPath))
	name := fmt.Sprintf("%s-%s.db", base, at.UTC().Format(backupStamp))
	return filepath.Join(filepath.Dir(dbPath), backupDir, name)
}

// Restore replaces the database at dst with the contents of src.
//
// The source is opened and checked first, so a path that is not a todo
// database, or is ahead of this build, is refused before anything at dst is
// touched. It is then written out with VACUUM INTO rather than copied, which
// folds any -wal into one file, and moved into place with a rename.
//
// An existing dst is left alone unless replace is true, in which case it is
// renamed aside and its path returned. Renaming rather than backing it up
// with VACUUM INTO is deliberate: the database being replaced may be the
// reason for the restore, and a plain rename works on one SQLite cannot open.
//
// Nothing may have dst open. SQLite has no way to tell a running process its
// file was replaced underneath it, so a `todo serve` left running alongside
// this would carry on reading the database that is no longer there.
func Restore(ctx context.Context, src, dst string, replace bool) (movedAside string, err error) {
	source, err := OpenStore(src)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", src, err)
	}
	defer source.Close()

	if _, err := os.Stat(dst); err == nil {
		if !replace {
			return "", fmt.Errorf("%s already exists; pass --replace to move it aside and restore over it", dst)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("checking %s: %w", dst, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return "", fmt.Errorf("creating the database directory: %w", err)
	}

	// Beside the destination rather than in a temp directory, so the rename
	// below stays on one filesystem and is therefore atomic.
	staged := fmt.Sprintf("%s.restoring-%s", dst, time.Now().UTC().Format(backupStamp))
	if _, err := source.db.ExecContext(ctx, "VACUUM INTO ?", staged); err != nil {
		return "", fmt.Errorf("staging the restore at %s: %w", staged, err)
	}
	defer os.Remove(staged) // a no-op once the rename below has moved it

	if _, err := os.Stat(dst); err == nil {
		movedAside = fmt.Sprintf("%s.replaced-%s", dst, time.Now().UTC().Format(backupStamp))
		if err := os.Rename(dst, movedAside); err != nil {
			return "", fmt.Errorf("moving %s aside: %w", dst, err)
		}
		// The old sidecars describe the database just moved away. Left here
		// they would be read as belonging to the restored one, which is how a
		// good backup turns into a corrupt database.
		for _, suffix := range sidecars {
			if err := os.Remove(dst + suffix); err != nil && !os.IsNotExist(err) {
				return movedAside, fmt.Errorf("removing %s: %w", dst+suffix, err)
			}
		}
	}

	if err := os.Rename(staged, dst); err != nil {
		return movedAside, fmt.Errorf("moving the restored database into place: %w", err)
	}
	return movedAside, nil
}
