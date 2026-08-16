package cli

import (
	"context"
	"errors"
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

// renderIndex builds the index the way `roz serve` does.
//
// It replaces `runCLI(t, "render", …)`: the command is gone, and the tests
// that used it were never about the command — they were about what the page
// says. Same two-value shape, so the assertions around it are untouched.
func renderIndex(t *testing.T, db string) (string, error) {
	t.Helper()
	return renderRouteFor(t, db, route{})
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

// TestActionsPageShowsOneView is #218: a view could be created, named and
// sorted, and the page had no way to be asked for it.
func TestActionsPageShowsOneView(t *testing.T) {
	db, ids := pageFixture(t)
	// A second action the view will leave out.
	other := addAction(t, db, "--title", "decide the thing", "--verb", "decide",
		"--project", ids.project)
	if _, err := runCLI(t, "view", "add", "writing", "--db", db,
		"--entity", "action", "--filter", `verb == "write"`,
		"--description", "What there is to write"); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	all := routeHTML(t, db, route{kind: pageActions})
	if !strings.Contains(all, ids.action) || !strings.Contains(all, other) {
		t.Fatalf("the unfiltered listing is missing an action:\n%s", all)
	}

	viewed := routeHTML(t, db, route{kind: pageActions, view: "writing"})
	if !strings.Contains(viewed, ids.action) {
		t.Errorf("the view dropped the action it selects:\n%s", viewed)
	}
	if strings.Contains(viewed, ">"+other+"<") {
		t.Errorf("the view kept an action it filters out:\n%s", viewed)
	}
}

// TestActionsPageOffersTheViewsThatExist. Without the picker, ?view= would be
// a parameter only somebody who had read the source would know to type.
func TestActionsPageOffersTheViewsThatExist(t *testing.T) {
	db, _ := pageFixture(t)
	if _, err := runCLI(t, "view", "add", "writing", "--db", db,
		"--entity", "action", "--filter", `verb == "write"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}
	// A project view, which belongs to another listing and not this picker.
	if _, err := runCLI(t, "view", "add", "mine", "--db", db,
		"--entity", "project", "--filter", `status != "done"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	all := routeHTML(t, db, route{kind: pageActions})
	if !strings.Contains(all, `href="/actions?view=writing"`) {
		t.Errorf("the picker does not offer the action view:\n%s", all)
	}
	if strings.Contains(all, "view=mine") {
		t.Errorf("the picker offers a view of another listing:\n%s", all)
	}
	// The listing's own order is what "all" means, and it is where we are.
	if !strings.Contains(all, `<span class="current"`) {
		t.Errorf("the picker does not mark where it is:\n%s", all)
	}

	viewed := routeHTML(t, db, route{kind: pageActions, view: "writing"})
	if !strings.Contains(viewed, `<span class="current" title="">writing</span>`) {
		t.Errorf("the picker does not mark the view in force:\n%s", viewed)
	}
	if !strings.Contains(viewed, `href="/actions"`) {
		t.Errorf("the picker offers no way back to everything:\n%s", viewed)
	}
}

// TestActionsPageSortsTheWayTheViewSays is the other half of #218: the page
// read a view's filter and then imposed its own order, so editing a view's
// sort changed the CLI and left the page alone.
func TestActionsPageSortsTheWayTheViewSays(t *testing.T) {
	db, ids := pageFixture(t)
	zebra := addAction(t, db, "--title", "zebra", "--verb", "write", "--project", ids.project)

	if _, err := runCLI(t, "view", "add", "alphabetical", "--db", db,
		"--entity", "action", "--filter", `verb == "write"`, "--sort", "title"); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	body := routeHTML(t, db, route{kind: pageActions, view: "alphabetical"})
	// "do the thing" before "zebra", which is not creation order.
	first, second := strings.Index(body, ids.action), strings.Index(body, zebra)
	if first < 0 || second < 0 {
		t.Fatalf("the view lost an action it selects:\n%s", body)
	}
	if first > second {
		t.Errorf("the page ignored the view's sort: %s came after %s", ids.action, zebra)
	}
}

// TestActionsPageRejectsAViewItCannotShow. A view named in a URL is a request,
// not a default: showing a different list would be a lie about which question
// was answered, so it is a wrong address instead.
func TestActionsPageRejectsAViewItCannotShow(t *testing.T) {
	db, _ := pageFixture(t)
	if _, err := runCLI(t, "view", "add", "mine", "--db", db,
		"--entity", "project", "--filter", `status != "done"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	for _, name := range []string{"nosuchview", "mine"} {
		_, err := renderRouteFor(t, db, route{kind: pageActions, view: name})
		if !errors.Is(err, errNoSuchPage) {
			t.Errorf("?view=%s returned %v, want a no-such-page error", name, err)
		}
	}
}

// TestProjectsPageShowsOneView. The same mechanism as the actions page, which
// is the point: /projects and /actions should not come to disagree about what
// naming a view means.
func TestProjectsPageShowsOneView(t *testing.T) {
	db, ids := pageFixture(t)
	if _, err := runCLI(t, "view", "add", "finished", "--db", db,
		"--entity", "project", "--filter", `status == "done"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	viewed := routeHTML(t, db, route{kind: pageProjects, view: "finished"})
	if !strings.Contains(viewed, ids.closed) {
		t.Errorf("the view dropped the project it selects:\n%s", viewed)
	}
	if strings.Contains(viewed, ">"+ids.project+"<") {
		t.Errorf("the view kept a project it filters out:\n%s", viewed)
	}
	if !strings.Contains(viewed, `href="/projects"`) {
		t.Errorf("the picker offers no way back to everything:\n%s", viewed)
	}

	// An action view is not this listing's, so it is a wrong address.
	if _, err := renderRouteFor(t, db, route{kind: pageProjects, view: "open_actions"}); !errors.Is(err, errNoSuchPage) {
		t.Errorf("an action view on /projects returned %v, want a no-such-page error", err)
	}
}

// TestProjectsPageDrawsTheTreeOnlyWithoutAView: indenting a project under a
// parent the view filtered out would draw a hierarchy that is not there.
func TestProjectsPageDrawsTheTreeOnlyWithoutAView(t *testing.T) {
	db, ids := pageFixture(t)
	if _, err := runCLI(t, "view", "add", "everything", "--db", db,
		"--entity", "project", "--filter", `id != ""`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	all := routeHTML(t, db, route{kind: pageProjects})
	if !strings.Contains(all, `class="depth1`) {
		t.Fatalf("the default listing does not indent %s under its parent:\n%s", ids.child, all)
	}
	viewed := routeHTML(t, db, route{kind: pageProjects, view: "everything"})
	if strings.Contains(viewed, `class="depth1`) {
		t.Errorf("a view drew the tree:\n%s", viewed)
	}
	// Same rows either way — this is about the drawing, not the selection.
	if !strings.Contains(viewed, ids.child) {
		t.Errorf("the flat listing lost a child project:\n%s", viewed)
	}
}
