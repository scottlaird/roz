package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/roz/internal/store"
)

// TestEveryPageWearsTheSameShell: the head, the heading and the stylesheet are
// written once, so a page nobody thought about still has them.
func TestEveryPageWearsTheSameShell(t *testing.T) {
	db, ids := pageFixture(t)

	for _, at := range []route{
		{},
		{kind: pageProjects},
		{kind: pageActions},
		{kind: pageProject, id: ids.project},
		{kind: pageAction, id: ids.action},
	} {
		name := at.kind
		if name == "" {
			name = "index"
		}
		t.Run(name, func(t *testing.T) {
			body := routeHTML(t, db, at)
			for _, want := range []string{
				"<!doctype html>",
				"<title>roz",
				`<h1><a href="/">roz</a>`,
				"<footer>",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("the page is missing %q from the shell", want)
				}
			}
		})
	}
}

// TestTheTrailSaysWhereYouAre. The last crumb is the page itself and carries
// no link, because a link to where you already are is furniture.
func TestTheTrailSaysWhereYouAre(t *testing.T) {
	db, ids := pageFixture(t)

	project := routeHTML(t, db, route{kind: pageProject, id: ids.project})
	for _, want := range []string{
		`<a href="/">roz</a>`,
		`<a href="/projects">projects</a>`,
		"<span>" + ids.project + "</span>",
	} {
		if !strings.Contains(project, want) {
			t.Errorf("the trail is missing %q:\n%s", want, crumbs(project))
		}
	}
	if strings.Contains(crumbs(project), `href="/project/`) {
		t.Error("the last crumb links to the page it is on")
	}

	// The index is the root, so it has no trail at all.
	if strings.Contains(routeHTML(t, db, route{}), `class="crumbs"`) {
		t.Error("the index carries a breadcrumb to itself")
	}
}

// TestALinkGoesToTheRowOrToThePage is the rule the issue asked for, and the
// reason the index could drop its appendix of everything.
//
// A reference to something on this page is an anchor — the row is right there.
// A reference to anything else leaves for that entity's own page rather than
// dangling, which is what the appendix used to prevent.
func TestALinkGoesToTheRowOrToThePage(t *testing.T) {
	db, ids := pageFixture(t)

	index := routeHTML(t, db, route{})
	if !strings.Contains(index, `href="#`+ids.project+`"`) {
		t.Errorf("a live project is on the index and was not anchored:\n%s", index)
	}
	if !strings.Contains(index, `href="/project/`+ids.closed+`"`) {
		t.Errorf("a closed project is not on the index and was not linked to its page:\n%s", index)
	}

	// The tooltip survives either way: it is what the identifier means, and
	// that does not depend on where the link goes.
	for _, link := range []string{
		`href="#` + ids.project + `" title=`,
		`href="/project/` + ids.closed + `" title=`,
	} {
		if !strings.Contains(index, link) {
			t.Errorf("a link lost its tooltip: no %q", link)
		}
	}
}

// TestTheIndexNoLongerCarriesEverything: what it leaves out has its own page
// now, so the appendix that existed to catch references is gone.
func TestTheIndexNoLongerCarriesEverything(t *testing.T) {
	db, ids := pageFixture(t)

	index := routeHTML(t, db, route{})
	if strings.Contains(index, "every other project") {
		t.Error("the index still appends every other project")
	}
	if strings.Contains(index, `<td class="n">`+ids.closed) {
		t.Error("a closed project still has a row on the index")
	}

	// And it is reachable, which is the whole trade.
	listing := routeHTML(t, db, route{kind: pageProjects})
	if !strings.Contains(listing, ids.closed) {
		t.Errorf("the projects page does not list a closed project:\n%s", listing)
	}
}

// TestAProjectPageShowsWhatItIsMadeOf.
func TestAProjectPageShowsWhatItIsMadeOf(t *testing.T) {
	db, ids := pageFixture(t)

	body := routeHTML(t, db, route{kind: pageProject, id: ids.project})
	if !strings.Contains(body, ids.child) {
		t.Errorf("the project page does not show its child:\n%s", body)
	}
	if !strings.Contains(body, `href="/project/`+ids.child+`"`) {
		t.Error("a child project is named but not linked to its own page")
	}
	// A project that is not its child gets no row of its own — it may still
	// be named by prose, which is a link rather than a row.
	if strings.Contains(body, `<td class="n"><a href="/project/`+ids.closed) {
		t.Errorf("an unrelated project got a row on the page:\n%s", body)
	}
}

// TestAnUnknownEntityIsNotFound: a mistyped identifier is a wrong address, not
// a broken server.
func TestAnUnknownEntityIsNotFound(t *testing.T) {
	db, _ := pageFixture(t)

	for _, at := range []route{
		{kind: pageProject, id: "SL999"},
		{kind: pageAction, id: "NA999"},
	} {
		if _, err := renderRouteFor(t, db, at); err == nil {
			t.Errorf("%s/%s rendered a page for something that does not exist", at.kind, at.id)
		}
	}
}

// pageIDs are the fixture's entities, named so a test can say what it means.
type pageIDs struct {
	project string // live, with a child and a prose reference to the closed one
	child   string
	closed  string
	action  string
}

// pageFixture builds a database with something in each state a page cares
// about: live work, a child, something finished, and prose that references
// both — so the anchor-or-link rule has both cases to decide.
func pageFixture(t *testing.T) (string, pageIDs) {
	t.Helper()
	db := initDB(t)

	var ids pageIDs
	ids.closed = addProject(t, db, "the finished one")
	if _, err := runCLI(t, "project", "close", "--db", db, ids.closed); err != nil {
		t.Fatalf("project close returned error: %v", err)
	}

	ids.project = addProject(t, db, "live work")
	if _, err := runCLI(t, "project", "set", "--db", db, ids.project,
		"--summary", "Follows on from "+ids.closed+"."); err != nil {
		t.Fatalf("project set returned error: %v", err)
	}
	ids.child = addProject(t, db, "part of it", "--parent", ids.project)
	ids.action = addAction(t, db, "--title", "do the thing", "--verb", "write",
		"--project", ids.project)
	return db, ids
}

func routeHTML(t *testing.T, db string, at route) string {
	t.Helper()
	body, err := renderRouteFor(t, db, at)
	if err != nil {
		t.Fatalf("rendering %s/%s: %v", at.kind, at.id, err)
	}
	return body
}

// renderRouteFor renders one route against a database, which is the
// programmatic way in that `roz render` does not offer for entity pages: they
// live at URLs, and a URL is not a file.
func renderRouteFor(t *testing.T, db string, at route) (string, error) {
	t.Helper()

	st, err := store.OpenStore(db)
	if err != nil {
		t.Fatalf("opening %s: %v", db, err)
	}
	defer st.Close()

	body, err := renderRoute(context.Background(), st, time.Now(), false, at)
	return string(body), err
}

// crumbs is the trail alone, for an error that shows the relevant line.
func crumbs(page string) string {
	_, rest, found := strings.Cut(page, `<nav class="crumbs">`)
	if !found {
		return "(no breadcrumb)"
	}
	trail, _, _ := strings.Cut(rest, "</nav>")
	return trail
}
