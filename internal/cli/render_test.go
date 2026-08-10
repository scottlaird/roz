package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderedFixture builds a database with something in every block, and
// something that each block should leave out.
func renderedFixture(t *testing.T) string {
	t.Helper()
	db := initDB(t)

	project := addProject(t, db, "Split the nodepool", "--priority", "1",
		"--effort", "weeks", "--jira-key", "CDSS-1744")
	if _, err := runCLI(t, "project", "jira", "--db", db, "CDSS-1744",
		"--status", "In Progress"); err != nil {
		t.Fatalf("project jira returned error: %v", err)
	}

	// A superseded project is not active, and should not be listed.
	retired := addProject(t, db, "Retire the old pool")
	if _, err := runCLI(t, "project", "supersede", "--db", db,
		"--from", retired, "--into", project); err != nil {
		t.Fatalf("project supersede returned error: %v", err)
	}

	ready := addAction(t, db, "--title", "Split the pool config", "--verb", "write",
		"--project", project, "--why", "everything else waits on it")
	blocked := addAction(t, db, "--title", "Roll the change out", "--verb", "run")
	hidden := addAction(t, db, "--title", "Tidy up after", "--verb", "write")
	if _, err := runCLI(t, "action", "add-blocker", "--db", db, "--from", blocked, "--to", ready); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}
	if _, err := runCLI(t, "action", "hide-behind", "--db", db, "--action", hidden, "--behind", ready); err != nil {
		t.Fatalf("action hide-behind returned error: %v", err)
	}

	// One window inside the fortnight and one well beyond it.
	addWindow(t, db, "oncall", "primary oncall", "2026-08-11", "2026-08-17")
	addWindow(t, db, "pto", "away", "2026-11-20", "2026-11-25")

	return db
}

func addWindow(t *testing.T, db, kind, label, starts, ends string) {
	t.Helper()
	if _, err := runCLI(t, "calendar", "add", "--db", db, "--kind", kind,
		"--label", label, "--starts", starts, "--ends", ends, "--capacity", "none"); err != nil {
		t.Fatalf("calendar add returned error: %v", err)
	}
}

func TestRender(t *testing.T) {
	db := renderedFixture(t)

	out, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	for _, want := range []string{
		"<pre>", "calendar", "queue", "projects",
		"primary oncall",        // inside the fortnight
		"Split the pool config", // unblocked
		"everything else waits on it",
		"CDSS-1744 (In Progress)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page does not contain %q:\n%s", want, out)
		}
	}

	for _, unwanted := range []string{
		"Roll the change out", // blocked
		"Tidy up after",       // hidden
		"Retire the old pool", // superseded
		"away",                // beyond the horizon
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the page contains %q, which belongs in none of the blocks:\n%s", unwanted, out)
		}
	}
}

func TestRenderToAFile(t *testing.T) {
	db := renderedFixture(t)
	path := filepath.Join(t.TempDir(), "status.html")

	out, err := runCLI(t, "render", "--db", db, "--out", path)
	if err != nil {
		t.Fatalf("render --out returned error: %v", err)
	}
	if !strings.Contains(out, path) {
		t.Errorf("render did not say where it wrote:\n%s", out)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the page: %v", err)
	}
	if !strings.Contains(string(written), "Split the pool config") {
		t.Errorf("the file does not hold the page:\n%s", written)
	}
}

// TestRenderEscapes: the blocks are inside <pre>, and a title is free text.
func TestRenderEscapes(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "fix <script>alert(1)</script> & friends", "--verb", "write")

	out, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("a title reached the page unescaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") || !strings.Contains(out, "&amp; friends") {
		t.Errorf("the title is missing or mangled:\n%s", out)
	}
}

// TestRenderEmpty: an empty queue must say so rather than render a bare
// header, which reads as something broken.
func TestRenderEmpty(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	for _, want := range []string{"nothing to do", "no active projects", "no calendar entries"} {
		if !strings.Contains(out, want) {
			t.Errorf("the empty page does not say %q:\n%s", want, out)
		}
	}
}
