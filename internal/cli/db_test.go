package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBBackupDefaultsBesideTheDatabase(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "db", "backup", "--db", db)
	if err != nil {
		t.Fatalf("db backup returned error: %v", err)
	}

	written := strings.TrimSpace(out)
	if got, want := filepath.Dir(written), filepath.Join(filepath.Dir(db), "backups"); got != want {
		t.Errorf("backup written to %s, want it in %s", got, want)
	}
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("the path it printed does not exist: %v", err)
	}
}

func TestDBBackupHonoursOut(t *testing.T) {
	db := initDB(t)
	want := filepath.Join(t.TempDir(), "elsewhere", "copy.db")

	out, err := runCLI(t, "db", "backup", "--db", db, "--out", want)
	if err != nil {
		t.Fatalf("db backup returned error: %v", err)
	}
	if got := strings.TrimSpace(out); got != want {
		t.Errorf("db backup printed %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("backup not written to --out: %v", err)
	}
}

// TestDBBackupAndRestoreRoundTrip is the whole point of the pair: what was
// there at backup time is what comes back, and what happened since does not.
func TestDBBackupAndRestoreRoundTrip(t *testing.T) {
	db := initDB(t)
	addProject(t, db, "before the backup")

	backup, err := runCLI(t, "db", "backup", "--db", db)
	if err != nil {
		t.Fatalf("db backup returned error: %v", err)
	}
	addProject(t, db, "after the backup")

	if _, err := runCLI(t, "db", "restore", strings.TrimSpace(backup), "--db", db, "--replace"); err != nil {
		t.Fatalf("db restore returned error: %v", err)
	}

	listed, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	if !strings.Contains(listed, "before the backup") {
		t.Errorf("restored database lost the project that was in the backup: %s", listed)
	}
	if strings.Contains(listed, "after the backup") {
		t.Errorf("restored database still has work done after the backup: %s", listed)
	}
}

func TestDBRestoreRefusesWithoutReplace(t *testing.T) {
	db := initDB(t)
	addProject(t, db, "the live one")

	backup, err := runCLI(t, "db", "backup", "--db", db)
	if err != nil {
		t.Fatalf("db backup returned error: %v", err)
	}

	_, err = runCLI(t, "db", "restore", strings.TrimSpace(backup), "--db", db)
	if err == nil {
		t.Fatal("db restore over an existing database returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "--replace") {
		t.Errorf("error = %v, want it to name the flag that would allow this", err)
	}
}

// TestDBRestoreSaysWhereTheOldOneWent: the replaced database is kept, and a
// message nobody can act on is the same as having deleted it.
func TestDBRestoreSaysWhereTheOldOneWent(t *testing.T) {
	db := initDB(t)
	backup, err := runCLI(t, "db", "backup", "--db", db)
	if err != nil {
		t.Fatalf("db backup returned error: %v", err)
	}

	out, err := runCLI(t, "db", "restore", strings.TrimSpace(backup), "--db", db, "--replace")
	if err != nil {
		t.Fatalf("db restore returned error: %v", err)
	}

	var kept string
	for _, line := range strings.Split(out, "\n") {
		if after, ok := strings.CutPrefix(line, "moved the previous database to "); ok {
			kept = after
		}
	}
	if kept == "" {
		t.Fatalf("db restore said %q, want it to name where the old database went", out)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the path it named does not exist: %v", err)
	}
}

// TestDBIsNotAnMCPTool: restoring replaces the database wholesale, which is
// not a decision an agent gets to make on its own.
func TestDBIsNotAnMCPTool(t *testing.T) {
	tools := (&mcpTools{db: "/tmp/todo.db", agent: "test"}).List()
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "db_") {
			t.Errorf("MCP offers %q, want the db commands left out", tool.Name)
		}
	}
}
