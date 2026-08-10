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

// TestTemplateIsTheOneOnDisk guards the embed: an edit to the .tmpl file
// should reach the page, and nothing should be answering from a copy in Go.
func TestTemplateIsTheOneOnDisk(t *testing.T) {
	onDisk, err := templates.ReadFile("templates/page.html.tmpl")
	if err != nil {
		t.Fatalf("reading the embedded template: %v", err)
	}
	for _, want := range []string{"{{.Calendar}}", "{{.Queue}}", "{{.Projects}}", "{{.GeneratedAt}}"} {
		if !strings.Contains(string(onDisk), want) {
			t.Errorf("the template does not use %s", want)
		}
	}

	db := initDB(t)
	out, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	// Nothing should reach the page as an unexpanded action.
	if strings.Contains(out, "{{") {
		t.Errorf("the page has an unexpanded action in it:\n%s", out)
	}
	for _, want := range []string{"<pre>", "<title>todo</title>"} {
		if !strings.Contains(out, want) {
			t.Errorf("the page is missing %q:\n%s", want, out)
		}
	}
}

// TestRenderSortsByPriority is the point of the page: the top of it should be
// what matters most, not what was typed first.
func TestRenderSortsByPriority(t *testing.T) {
	db := initDB(t)

	// Added in the wrong order on purpose.
	low := addProject(t, db, "the later one", "--priority", "4")
	addProject(t, db, "nobody said")
	high := addProject(t, db, "the urgent one", "--priority", "1")

	addAction(t, db, "--title", "slow work", "--verb", "write", "--project", low)
	addAction(t, db, "--title", "loose work", "--verb", "write")
	addAction(t, db, "--title", "urgent work", "--verb", "write", "--project", high)

	out, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	assertOrder(t, out, "the queue", "urgent work", "slow work", "loose work")
	assertOrder(t, out, "the projects", "the urgent one", "the later one", "nobody said")
}

// assertOrder checks the wanted strings appear in the given order.
func assertOrder(t *testing.T, page, what string, want ...string) {
	t.Helper()

	at := -1
	for _, s := range want {
		i := strings.Index(page, s)
		if i < 0 {
			t.Errorf("%s: %q is not on the page:\n%s", what, s, page)
			return
		}
		if i < at {
			t.Errorf("%s: %q comes too late:\n%s", what, s, page)
			return
		}
		at = i
	}
}

// TestListsStillPrintInCreationOrder: only the page is ranked, so nothing
// else should have moved.
func TestListsStillPrintInCreationOrder(t *testing.T) {
	db := initDB(t)
	low := addProject(t, db, "the later one", "--priority", "4")
	addProject(t, db, "the urgent one", "--priority", "1")
	addAction(t, db, "--title", "slow work", "--verb", "write", "--project", low)
	addAction(t, db, "--title", "urgent work", "--verb", "write")

	projects, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	assertOrder(t, projects, "project list", "the later one", "the urgent one")

	actions, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	assertOrder(t, actions, "action list", "slow work", "urgent work")
}

// TestListSortFlag: the lists default to creation order and take priority on
// request, which is the whole of the flag.
func TestListSortFlag(t *testing.T) {
	db := initDB(t)
	later := addProject(t, db, "the later one", "--priority", "4")
	urgent := addProject(t, db, "the urgent one", "--priority", "1")
	addAction(t, db, "--title", "slow work", "--verb", "write", "--project", later)
	addAction(t, db, "--title", "urgent work", "--verb", "write", "--project", urgent)

	tests := []struct {
		name  string
		args  []string
		first string
		then  string
	}{
		{"projects, default", []string{"project", "list"}, "the later one", "the urgent one"},
		{"projects, priority", []string{"project", "list", "--sort", "priority"},
			"the urgent one", "the later one"},
		{"actions, default", []string{"action", "list"}, "slow work", "urgent work"},
		{"actions, priority", []string{"action", "list", "--sort", "priority"},
			"urgent work", "slow work"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runCLI(t, append(tt.args, "--db", db)...)
			if err != nil {
				t.Fatalf("%v returned error: %v", tt.args, err)
			}
			assertOrder(t, out, tt.name, tt.first, tt.then)
		})
	}
}

func TestListSortRejectsAnUnknownOrder(t *testing.T) {
	db := initDB(t)

	for _, args := range [][]string{
		{"action", "list", "--sort", "whenever"},
		{"project", "list", "--sort", "whenever"},
	} {
		_, err := runCLI(t, append(args, "--db", db)...)
		if err == nil {
			t.Fatalf("%v accepted an unknown order, want an error", args)
		}
		if !strings.Contains(err.Error(), "is not an order") {
			t.Errorf("error = %v", err)
		}
	}
}

// TestSortPriorityStillFilters: the ordering must not change which rows come
// back, only their order.
func TestSortPriorityStillFilters(t *testing.T) {
	db := initDB(t)
	ready := addAction(t, db, "--title", "ready work", "--verb", "write")
	blocked := addAction(t, db, "--title", "blocked work", "--verb", "run")
	if _, err := runCLI(t, "action", "add-blocker", "--db", db, "--from", blocked, "--to", ready); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}

	out, err := runCLI(t, "action", "list", "--db", db, "--unblocked", "--sort", "priority")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(out, "ready work") || strings.Contains(out, "blocked work") {
		t.Errorf("--unblocked --sort priority returned the wrong rows:\n%s", out)
	}
}
