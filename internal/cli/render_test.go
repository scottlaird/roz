package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/static"
)

// renderedFixture builds a database with something in every block, and
// something that each block should leave out.
func renderedFixture(t *testing.T) string {
	t.Helper()
	db := initDB(t)

	project := addProject(t, db, "Split the nodepool", "--priority", "1",
		"--effort", "weeks", "--issue", "CDSS-1744")
	if _, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-1744",
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
	addWindow(t, db, "pto", "november pto", "2026-11-20", "2026-11-25")

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

	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	for _, want := range []string{
		"next fortnight", "queue", "projects",
		"primary oncall",        // inside the fortnight
		"Split the pool config", // unblocked
		"everything else waits on it",
		"CDSS-1744", "(In Progress)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page does not contain %q:\n%s", want, out)
		}
	}

	// The index shows live work. What it leaves out -- closed, blocked,
	// hidden -- is no longer appended to the bottom of it: each of those has
	// its own page now, and a reference to one leaves rather than jumping.
	for _, unwanted := range []string{
		"Roll the change out", // blocked
		"Tidy up after",       // hidden
		"Retire the old pool", // superseded
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the page still carries %q, which belongs on its own page now:\n%s",
				unwanted, out)
		}
	}

	// A calendar window beyond the horizon is not an entity anything links to,
	// so it stays off the page entirely.
	//
	// Its own label rather than a word that might occur anywhere: the page
	// carries a stylesheet full of English, and "away" appears in a comment in
	// it, which made this assertion fail for a reason that had nothing to do
	// with calendars.
	if strings.Contains(out, "november pto") {
		t.Errorf("the page contains a window beyond the horizon:\n%s", out)
	}
}

// TestRenderEscapes: the blocks are inside <pre>, and a title is free text.
func TestRenderEscapes(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "fix <script>alert(1)</script> & friends", "--verb", "write")

	out, err := renderIndex(t, db)
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

// TestRenderProseIsMarkdown: why is prose and renders as the author wrote it;
// the title beside it is a name, so the same characters stay literal there.
func TestRenderProseIsMarkdown(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "the *old* pipeline", "--verb", "write",
		"--why", "unblocks the *split*, once `roz sync github` runs")

	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	for _, want := range []string{
		"<em>split</em>",
		"<code>roz sync github</code>",
		"the *old* pipeline",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<em>old</em>") {
		t.Errorf("the title was rendered as Markdown:\n%s", out)
	}
}

// TestRenderEmpty: an empty queue must say so rather than render a bare
// header, which reads as something broken.
func TestRenderEmpty(t *testing.T) {
	db := initDB(t)

	out, err := renderIndex(t, db)
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
	shell, err := templates.ReadFile("templates/shell.html.tmpl")
	if err != nil {
		t.Fatalf("reading the embedded shell: %v", err)
	}
	index, err := templates.ReadFile("templates/index.html.tmpl")
	if err != nil {
		t.Fatalf("reading the embedded index: %v", err)
	}
	for _, want := range []string{"{{.Stamp}}", "{{.GeneratedAt}}", `{{template "body" .}}`} {
		if !strings.Contains(string(shell), want) {
			t.Errorf("the shell does not use %s", want)
		}
	}
	for _, want := range []string{"range .Queue", "range .Projects"} {
		if !strings.Contains(string(index), want) {
			t.Errorf("the index body does not use %s", want)
		}
	}

	db := initDB(t)
	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	// Nothing should reach the page as an unexpanded action.
	if strings.Contains(out, "{{") {
		t.Errorf("the page has an unexpanded action in it:\n%s", out)
	}
	for _, want := range []string{"<title>roz</title>", "nothing to do"} {
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

	out, err := renderIndex(t, db)
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
		if !strings.Contains(err.Error(), "is not a column to sort on") {
			t.Errorf("error = %v", err)
		}
		// The rankings are still valid values, so the error has to offer them
		// as well as the columns.
		if !strings.Contains(err.Error(), sortPriority) {
			t.Errorf("error does not offer the rankings: %v", err)
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

// TestRenderReadsTheConfiguredJira is the point of the config table: linking
// used to need a flag on every invocation, so in practice the page rendered
// with no Jira at all.
func TestRenderReadsTheConfiguredJira(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "Close CDSS-1557", "--verb", "write",
		"--why", "blocked on CDSS-1557")

	before, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if strings.Contains(before, "<a href=\"https://example.atlassian.net") {
		t.Errorf("an unconfigured database linked a key:\n%s", before)
	}

	if _, err := runCLI(t, "config", "set", "--db", db,
		"--jira-base-url", "https://example.atlassian.net/browse",
		"--jira-prefix", "CDSS"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}

	after, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	want := `<a href="https://example.atlassian.net/browse/CDSS-1557">CDSS-1557</a>`
	if strings.Count(after, want) != 2 {
		t.Errorf("want the key linked in both the title and the why:\n%s", after)
	}
}

// TestRenderShowsTheOwner: unset is the normal case, and the heading should
// read the way it always did rather than trailing a separator.
func TestRenderShowsTheOwner(t *testing.T) {
	db := initDB(t)

	before, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(before, `<h1><a href="/">roz</a></h1>`) {
		t.Errorf("an unconfigured page has an odd heading:\n%s", before)
	}

	if _, err := runCLI(t, "config", "set", "--db", db, "--owner", "scott"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}
	after, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(after, "scott") || !strings.Contains(after, "<title>roz · scott</title>") {
		t.Errorf("the owner is missing from the page:\n%s", after)
	}
}

// TestRenderPrefersTheEnvironment: the one override left, for rendering
// against a different Jira without writing that decision into the database.
func TestRenderPrefersTheEnvironment(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "Close CDSS-1557", "--verb", "write")
	if _, err := runCLI(t, "config", "set", "--db", db,
		"--jira-base-url", "https://stored.example.com/browse",
		"--jira-prefix", "CDSS"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}

	t.Setenv("ROZ_JIRA_BASE_URL", "https://override.example.com/browse")
	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(out, "https://override.example.com/browse/CDSS-1557") {
		t.Errorf("ROZ_JIRA_BASE_URL did not override the stored value:\n%s", out)
	}
	if strings.Contains(out, "stored.example.com") {
		t.Errorf("the stored base URL was used as well:\n%s", out)
	}
}

// TestRenderProjectSummary: project.summary carries format:"markdown", and
// the page is the only thing that renders it — so without this the tag on
// that column is not exercised anywhere.
func TestRenderProjectSummary(t *testing.T) {
	db := initDB(t)
	addProject(t, db, "Rank the queue", "--summary",
		"**Ranking**, then `unblocks_count`.\n\n- first\n- second")
	addProject(t, db, "No summary here")

	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	for _, want := range []string{
		"<strong>Ranking</strong>",
		"<code>unblocks_count</code>",
		"<li>first</li>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page does not contain %q:\n%s", want, out)
		}
	}

	// One row per summary, and none for the project without one: a blank row
	// under every project would be a rule drawn across the table for nothing.
	if n := strings.Count(out, `class="detail`); n != 1 {
		t.Errorf("the page has %d summary rows, want 1", n)
	}
}

// TestRenderProjectTitleStaysLiteral: the title beside the summary is a name,
// so it is escaped and linked but never parsed as Markdown.
func TestRenderProjectTitleStaysLiteral(t *testing.T) {
	db := initDB(t)
	addProject(t, db, "the *old* pipeline", "--summary", "replaced by the *new* one")

	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(out, "the *old* pipeline") {
		t.Errorf("the title was interpreted as Markdown:\n%s", out)
	}
	if !strings.Contains(out, "the <em>new</em> one") {
		t.Errorf("the summary was not rendered:\n%s", out)
	}
}

// TestPageCarriesItsFavicon: the icon is inline because the page is one file,
// so the thing to check is that it is actually there and actually a PNG.
//
// The ZgotmplZ assertion is the one that earns its place. html/template
// rejects a data: href as an unsafe scheme and silently substitutes that
// placeholder, so typing Favicon as a plain string produces a page that is
// valid, renders fine, and quietly has no icon.
func TestPageCarriesItsFavicon(t *testing.T) {
	db := initDB(t)

	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(out, `<link rel="icon" type="image/png"`) {
		t.Error("the page has no icon link")
	}
	if !strings.Contains(out, "href=\"data:image/png;base64,iVBORw0KGgo") {
		t.Error("the icon is not an inline PNG data URI")
	}
	if strings.Contains(out, "ZgotmplZ") {
		t.Error("html/template blanked a URL: Favicon needs to be a template.URL, not a string")
	}
}

// TestPageShowsAnExpiredSnooze: the template has styled `.expired` and the
// chip has said "due" since before this was reachable — the queue query simply
// never let such a row through. This is the end-to-end check that it does now.
func TestPageShowsAnExpiredSnooze(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "past its date", "--verb", "decide")
	if _, err := runCLI(t, "action", "snooze", id, "--db", db,
		"--snooze-until", "2000-01-01", "--snooze-reason", "Sprint 148"); err != nil {
		t.Fatalf("action snooze returned error: %v", err)
	}

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(page, "past its date") {
		t.Fatalf("the expired snooze is not on the page:\n%s", page)
	}
	if !strings.Contains(page, "expired") {
		t.Error("the row is on the page but nothing marks it as expired")
	}
	if !strings.Contains(page, "due 2000-01-01") {
		t.Error("the page does not say the date it came due")
	}
}

// TestPageLeavesALiveSnoozeHidden is the other half: deferring work still
// defers it, or this would have turned the snooze into a no-op.
func TestPageLeavesALiveSnoozeHidden(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "still deferred", "--verb", "decide")
	if _, err := runCLI(t, "action", "snooze", id, "--db", db,
		"--snooze-until", "2099-01-01"); err != nil {
		t.Fatalf("action snooze returned error: %v", err)
	}

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	// Deferring means out of the queue, not out of existence. It is off the
	// index entirely now, and reachable at its own page — what a snooze
	// promises is that it is not competing for attention, not that it is gone.
	if strings.Contains(page, "still deferred") {
		t.Errorf("a snooze that has not expired reached the index:\n%s", page)
	}
}

// TestPageTooltipsLinksFromTheDatabase: the titles are already stored, and a
// reader hovering an identifier should not have to follow it to learn what it
// is.
func TestPageTooltipsLinksFromTheDatabase(t *testing.T) {
	db, key := trackedPR(t)
	withFetcher(t, stubFetcher{result: github.Result{
		PullRequests: []github.PullRequest{{
			Key: key, Repo: "owner/repo", Number: 1,
			Title: "Retry the upstream call on 503", State: "OPEN", BaseRef: "main",
		}},
	}})
	if _, err := runCLI(t, "sync", "github", "--db", db, "--quiet"); err != nil {
		t.Fatalf("sync returned error: %v", err)
	}

	// One reference the linker finds itself, and one written out by hand with
	// its own display text.
	addAction(t, db, "--title", "look at "+key, "--verb", "decide",
		"--why", "blocked on [that one](https://github.com/owner/repo/pull/1)")

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if got := strings.Count(page, `title="Retry the upstream call on 503"`); got != 2 {
		t.Errorf("%d captioned links, want both the auto-linked and the hand-written one:\n%s",
			got, page)
	}
	// The author's display text survives.
	if !strings.Contains(page, ">that one<") {
		t.Errorf("the hand-written link lost its text:\n%s", page)
	}
}

// TestPageLeavesUntrackedLinksBare: absent rather than guessed.
func TestPageLeavesUntrackedLinksBare(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "look at owner/other#99", "--verb", "decide")

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if strings.Contains(page, "title=") {
		t.Errorf("an untracked pull request was captioned:\n%s", page)
	}
	// Still linked, though — the link is right, only the caption is unknown.
	if !strings.Contains(page, "owner/other/pull/99") {
		t.Errorf("the link itself is missing:\n%s", page)
	}
}

// TestPageAnchorsEveryEntityExactlyOnce is the constraint the split exists
// for: an id must be unique in a document, so an action cannot be anchored
// both in the queue and in an index of everything.
func TestPageAnchorsEveryEntityExactlyOnce(t *testing.T) {
	db := renderedFixture(t)

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	ids := map[string]int{}
	for _, m := range regexp.MustCompile(`id="((?:NA|SL)\d+)"`).FindAllStringSubmatch(page, -1) {
		ids[m[1]]++
	}
	if len(ids) == 0 {
		t.Fatalf("nothing on the page is anchored:\n%s", page)
	}
	for id, n := range ids {
		if n != 1 {
			t.Errorf("%s is anchored %d times, want exactly once", id, n)
		}
	}
}

// TestPageLinksAnActionToItsProject: the column named a project and sat a few
// hundred pixels above it without linking.
func TestPageLinksAnActionToItsProject(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "the project")
	addAction(t, db, "--title", "do it", "--verb", "decide", "--project", project)

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(page, `href="#`+project+`"`) {
		t.Errorf("the action's project is not a link:\n%s", page)
	}
	if !strings.Contains(page, `id="`+project+`"`) {
		t.Errorf("the link has no anchor to land on:\n%s", page)
	}
	// And it says what it points at, like every other reference on the page.
	// This one was built by hand and so was the only identifier without a
	// tooltip — the most-clicked link there, and the least explained.
	if !strings.Contains(page, `title="the project"`) {
		t.Errorf("the project link has no tooltip:\n%s", page)
	}
}

// TestPageLinksReferencesInProse, and only the ones that exist.
func TestPageLinksReferencesInProse(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "the target")
	addAction(t, db, "--title", "do it", "--verb", "decide",
		"--why", "waits for "+project+", and for SL999 which is nobody")

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(page, `href="#`+project+`"`) {
		t.Errorf("a reference to a real project did not link:\n%s", page)
	}
	if strings.Contains(page, `href="#SL999"`) {
		t.Errorf("a reference to a project that does not exist became a link:\n%s", page)
	}
	if !strings.Contains(page, "SL999") {
		t.Errorf("the unresolvable reference lost its text:\n%s", page)
	}
}

// TestATrackerIssueIsALink: the column held the key as plain text, so the one
// thing a reader wants from it — going to the issue — meant copying a string
// into a search box.
func TestATrackerIssueIsALink(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "Track the issues")
	linkIssue(t, db, project, "github", "scottlaird/roz#101")
	linkIssue(t, db, project, "jira", "CDSS-1744")
	if _, err := runCLI(t, "config", "set", "--db", db,
		"--jira-base-url", "https://example.atlassian.net/browse",
		"--jira-prefix", "CDSS"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}

	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	// /issues/ rather than /pull/: the row says which kind it is.
	for _, want := range []string{
		`href="https://github.com/scottlaird/roz/issues/101"`,
		`href="https://example.atlassian.net/browse/CDSS-1744"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page does not link %s:\n%s", want, out)
		}
	}
}

// TestAGitHubIssueUsesTheShortName: what registering a short name says is that
// this is what the repository is called around here.
func TestAGitHubIssueUsesTheShortName(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "Track the issues")
	linkIssue(t, db, project, "github", "scottlaird/roz#101")
	linkIssue(t, db, project, "github", "someone/else#7")

	before, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(before, ">scottlaird/roz#101<") {
		t.Errorf("an unregistered repository should render in full:\n%s", before)
	}

	if _, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/roz",
		"--short-name", "roz"); err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}
	after, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(after, ">roz#101<") {
		t.Errorf("the short name is not used:\n%s", after)
	}
	if strings.Contains(after, ">scottlaird/roz#101<") {
		t.Errorf("the full name is still drawn:\n%s", after)
	}
	// The href is the repository's real address either way, and a repository
	// with no short name keeps its full one.
	if !strings.Contains(after, `href="https://github.com/scottlaird/roz/issues/101"`) {
		t.Errorf("the link no longer addresses the repository:\n%s", after)
	}
	if !strings.Contains(after, ">someone/else#7<") {
		t.Errorf("a repository with no short name should be unchanged:\n%s", after)
	}
}

// TestATrackerIssueCarriesItsSummary is #160: the summary is stored and was
// not reaching the anchor, so a key said which issue only if you knew it.
func TestATrackerIssueCarriesItsSummary(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "Track the issues")
	linkIssue(t, db, project, "jira", "CDSS-1744")
	linkIssue(t, db, project, "jira", "CDSS-9999")
	if _, err := runCLI(t, "config", "set", "--db", db,
		"--jira-base-url", "https://example.atlassian.net/browse",
		"--jira-prefix", "CDSS"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}
	if _, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-1744",
		"--summary", "Allow scaling walker cells up", "--status", "In Progress"); err != nil {
		t.Fatalf("issue observe returned error: %v", err)
	}

	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(out, `title="Allow scaling walker cells up"`) {
		t.Errorf("the summary is not on the anchor:\n%s", out)
	}
	// Absent rather than blank: a link with an empty tooltip claims roz looked
	// and found nothing.
	if strings.Contains(out, `title=""`) {
		t.Errorf("an unobserved issue got an empty tooltip:\n%s", out)
	}
	// The status stays its own span rather than merging into the tooltip.
	if !strings.Contains(out, `<span class="state">(In Progress)</span>`) {
		t.Errorf("the status span is gone:\n%s", out)
	}
}

// linkIssue attaches a tracker issue to a project.
func linkIssue(t *testing.T, db, project, tracker, key string) {
	t.Helper()
	if _, err := runCLI(t, "project", "link-issue", "--db", db,
		"--project", project, "--tracker", tracker, "--issue", key); err != nil {
		t.Fatalf("project link-issue returned error: %v", err)
	}
}

// TestAPullRequestChipUsesTheShortName: the same rule as an issue's, since
// both are roz writing an identifier rather than repeating one somebody typed.
func TestAPullRequestChipUsesTheShortName(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/roz",
		"--short-name", "roz"); err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}
	if _, err := runCLI(t, "pr", "track", "--db", db, "scottlaird/roz#39"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	addAction(t, db, "--title", "Review it", "--verb", "review", "--pr", "scottlaird/roz#39")

	out, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(out, ">roz#39<") {
		t.Errorf("the chip does not use the short name:\n%s", out)
	}
}

// TestIssueCellKeepsAKeyWithItsState is scottlaird/roz#161. A tracker key and
// its state are one thing to read, and a long status split them across two
// lines: "(Waiting for Security" over "Review)" is not two pieces of
// information.
//
// Asserted on the markup rather than on a width, because a width is a number
// tied to today's content and this is the property that has to hold: the pair
// is one unbreakable run, whatever the column turns out to be.
func TestIssueCellKeepsAKeyWithItsState(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "Migrate the auth service", "--issue", "CDSS-4242")
	if _, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-4242",
		"--status", "Waiting for Security Review"); err != nil {
		t.Fatalf("issue observe returned error: %v", err)
	}
	addAction(t, db, "--title", "Do it", "--verb", "write", "--project", project)

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	if !strings.Contains(page, `class="issue"`) {
		t.Errorf("the key and its state are not held together:\n%s", page)
	}
	// And the rule that keeps that run whole is in the stylesheet, which the
	// page links rather than carries.
	css, err := static.Read("roz.css")
	if err != nil {
		t.Fatalf("reading the stylesheet: %v", err)
	}
	if !strings.Contains(css, ".issue{white-space:nowrap}") {
		t.Error("the stylesheet does not keep an issue on one line")
	}
	// The pair is inside one element, rather than the state trailing outside
	// it where a break could still fall between them.
	pair := regexp.MustCompile(`<span class="issue">.*?\(Waiting for Security Review\).*?</span>`)
	if !pair.MatchString(page) {
		t.Errorf("the state sits outside the run that holds the key:\n%s", page)
	}
}

// TestIssueListHasNoStrayGapBeforeItsComma: the newlines around a template
// definition are text, and they were reaching the page — so two issues in a
// cell read "CDSS-1 (Open) , CDSS-2 (Open)".
func TestIssueListHasNoStrayGapBeforeItsComma(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "Split the nodepool", "--issue", "CDSS-1744")
	if _, err := runCLI(t, "project", "link-issue", "--db", db,
		"--project", project, "--issue", "CDSS-1745"); err != nil {
		t.Fatalf("project link-issue returned error: %v", err)
	}
	addAction(t, db, "--title", "Do it", "--verb", "write", "--project", project)

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if strings.Contains(page, " , ") || strings.Contains(page, "\n, ") {
		t.Errorf("an issue list carries a stray gap before its comma:\n%s",
			issueCell(t, page))
	}
	if !strings.Contains(page, "</span>, <span") {
		t.Errorf("two issues are not separated by a plain comma:\n%s", issueCell(t, page))
	}
}

// issueCell pulls out the first issues cell, for an error worth reading.
func issueCell(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<td><span class="issue">.*?</td>`).FindString(page)
	if m == "" {
		return page
	}
	return m
}
