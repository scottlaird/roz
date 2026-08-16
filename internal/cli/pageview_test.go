package cli

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestThePageFollowsItsView is the point of seeding them: what the page means
// by "live work" is a row somebody can edit rather than a filter compiled in.
func TestThePageFollowsItsView(t *testing.T) {
	db := initDB(t)
	live := addProject(t, db, "live work")
	blocked := addProject(t, db, "blocked work")
	if _, err := runCLI(t, "project", "block", "--db", db,
		"--from", blocked, "--to", live); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	// Seeded: both are live work, blocked included.
	before := pageHTML(t, db)
	if !strings.Contains(before, live) || !strings.Contains(before, blocked) {
		t.Fatalf("the seeded view did not show both projects:\n%s", projectsTable(before))
	}

	// Narrow the view, and the page narrows with it — no rebuild.
	if _, err := runCLI(t, "view", "add", "--db", db, "open_projects",
		"--entity", "project", "--filter", `status == "blocked"`,
		"--sort", "priority"); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}
	after := projectsTable(pageHTML(t, db))
	if strings.Contains(after, live) {
		t.Errorf("the page ignored the narrowed view:\n%s", after)
	}
	if !strings.Contains(after, blocked) {
		t.Errorf("the page dropped what the view selects:\n%s", after)
	}
}

// TestThePageSurvivesALostView: a view is a row somebody can drop, and the
// page going blank is the worst way to find that out. The fallback is the
// definition the view was seeded from, so the page shows what it always did.
func TestThePageSurvivesALostView(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "live work")

	if _, err := runCLI(t, "view", "drop", "--db", db, "open_projects"); err != nil {
		t.Fatalf("view drop returned error: %v", err)
	}
	if page := pageHTML(t, db); !strings.Contains(page, project) {
		t.Errorf("the page lost its projects with the view:\n%s", projectsTable(page))
	}

	// And a view that no longer compiles is the same case: broken by a
	// migration is indistinguishable from missing, and both fall back.
	if _, err := runCLI(t, "view", "add", "--db", db, "open_projects",
		"--entity", "project", "--filter", `status != "done"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}
	breakView(t, db, "open_projects")
	if page := pageHTML(t, db); !strings.Contains(page, project) {
		t.Errorf("a broken view emptied the page:\n%s", projectsTable(page))
	}
}

// breakView writes an expression past the command's validation, standing in
// for a migration that renamed the column underneath a saved view.
func breakView(t *testing.T, db, name string) {
	t.Helper()

	handle, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("opening %s: %v", db, err)
	}
	defer handle.Close()

	if _, err := handle.Exec(
		`UPDATE view SET filter = 'gone_column == "done"' WHERE name = ?`, name); err != nil {
		t.Fatalf("breaking view %s: %v", name, err)
	}
}

func pageHTML(t *testing.T, db string) string {
	t.Helper()
	out, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	return out
}

// projectsTable is the part of the page a project view decides, for an error
// that shows the relevant half rather than the whole document.
func projectsTable(page string) string {
	_, rest, found := strings.Cut(page, "<h2>projects</h2>")
	if !found {
		return page
	}
	table, _, _ := strings.Cut(rest, "</table>")
	return table
}

// TestRenderStandsAlone: the file `roz render` writes is opened from disk as
// often as it is served, and a linked stylesheet has nowhere to be fetched
// from there. The served page links it instead and is told 304.
func TestRenderStandsAlone(t *testing.T) {
	db := initDB(t)
	addProject(t, db, "live work")

	written := pageHTML(t, db)
	if !strings.Contains(written, "<style>") {
		t.Error("the written page does not carry its stylesheet")
	}
	if strings.Contains(written, `href="/static/`) {
		t.Error("the written page links a stylesheet it cannot fetch")
	}
	// The CSS is really there, not an empty tag.
	if !strings.Contains(written, "--paper:") {
		t.Error("the inlined stylesheet is empty")
	}
}
