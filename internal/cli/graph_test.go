package cli

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/roz/internal/store"
)

// graphFor builds the diagram the index would draw for a database.
func graphFor(t *testing.T, db string) *dependencyGraph {
	t.Helper()
	ctx := context.Background()

	st, err := store.OpenStore(db)
	if err != nil {
		t.Fatalf("OpenStore() returned error: %v", err)
	}
	defer st.Close()

	cfg, err := pageSettings(ctx, st)
	if err != nil {
		t.Fatalf("pageSettings() returned error: %v", err)
	}
	content, err := buildRoute(ctx, st, time.Now(), false, cfg, route{kind: pageGraph})
	if err != nil {
		t.Fatalf("buildRoute() returned error: %v", err)
	}
	if content.Graph == nil {
		t.Fatal("the graph page built no graph")
	}
	return content.Graph
}

func nodeIDs(g *dependencyGraph) []string {
	ids := make([]string, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[i] = n.ID
	}
	return ids
}

func hasNode(g *dependencyGraph, id string) bool {
	for _, n := range g.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

func hasEdge(g *dependencyGraph, from, to, kind string) bool {
	for _, e := range g.Edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return true
		}
	}
	return false
}

// TestAQueueWithNoDependenciesDrawsNothing. Empty is a real answer and not a
// failure: most queues record no blocking at all, and a page that responded by
// drawing every project as an unconnected box would be strictly worse than the
// list above it.
func TestAQueueWithNoDependenciesDrawsNothing(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "Split the nodepool")
	addAction(t, db, "--title", "do it", "--verb", "write", "--project", project)

	g := graphFor(t, db)
	if !g.Empty() {
		t.Errorf("drew %v with nothing blocked on anything", nodeIDs(g))
	}
	if g.Mermaid != "" {
		t.Errorf("emitted diagram source for an empty graph:\n%s", g.Mermaid)
	}
	// And says what it left out, so "small" and "broken" are distinguishable.
	if g.OmittedProjects != 1 || g.OmittedActions != 1 {
		t.Errorf("omitted %d projects and %d actions, want 1 and 1",
			g.OmittedProjects, g.OmittedActions)
	}
}

// TestBlockingSeedsTheGraph, and pulls in the work that is actually holding
// things up: the layer the flat list has no room for.
func TestBlockingSeedsTheGraph(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "Infer reviewers")
	blocked := addProject(t, db, "The review_rule entity")
	if _, err := runCLI(t, "project", "block", "--db", db,
		"--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}
	work := addAction(t, db, "--title", "read CODEOWNERS", "--verb", "write",
		"--project", blocker)
	elsewhere := addProject(t, db, "Something unrelated")

	g := graphFor(t, db)
	if !hasEdge(g, blocker, blocked, edgeBlocks) {
		t.Errorf("no blocking edge %s -> %s in %v", blocker, blocked, g.Edges)
	}
	if !hasNode(g, work) || !hasEdge(g, work, blocker, edgeAdvances) {
		t.Errorf("the open action holding %s up is not attached: %v", blocker, nodeIDs(g))
	}
	if hasNode(g, elsewhere) {
		t.Errorf("drew %s, which is blocked on nothing and blocks nothing", elsewhere)
	}
	if g.OmittedProjects != 1 {
		t.Errorf("omitted %d projects, want 1", g.OmittedProjects)
	}
}

// TestASatisfiedBlockerIsNotDrawn. Edges are never deleted, so every chain
// ever completed is still in the database; drawing them answers "how did we
// get here" at the cost of never answering "what now".
func TestASatisfiedBlockerIsNotDrawn(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "The thing that finished")
	blocked := addProject(t, db, "The thing that did not")
	if _, err := runCLI(t, "project", "block", "--db", db,
		"--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}
	if _, err := runCLI(t, "project", "close", "--db", db, blocker); err != nil {
		t.Fatalf("project close returned error: %v", err)
	}

	g := graphFor(t, db)
	if hasNode(g, blocker) {
		t.Errorf("drew %s, which is closed and holds nothing up: %v", blocker, nodeIDs(g))
	}
	if !g.Empty() {
		t.Errorf("drew %v; the only edge is satisfied, so there is nothing left", nodeIDs(g))
	}
}

// TestContainmentIsContextRatherThanASeed. A parent is not a blocker — the
// flag that sets one says so — so starting from containment would draw every
// project that happens to be part of something, which is most of them.
func TestContainmentIsContextRatherThanASeed(t *testing.T) {
	db := initDB(t)
	parent := addProject(t, db, "Improve review management")
	blocker := addProject(t, db, "Infer reviewers", "--parent", parent)
	blocked := addProject(t, db, "The review_rule entity", "--parent", parent)
	// A sibling with no dependency at all, to prove the parent does not drag
	// the family in behind it.
	sibling := addProject(t, db, "A third child", "--parent", parent)

	if _, err := runCLI(t, "project", "block", "--db", db,
		"--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	g := graphFor(t, db)
	if !hasNode(g, parent) {
		t.Errorf("the parent is missing, so the group reads as three loose projects: %v", nodeIDs(g))
	}
	if !hasEdge(g, parent, blocker, edgeContains) {
		t.Errorf("no containment edge %s -> %s in %v", parent, blocker, g.Edges)
	}
	if hasNode(g, sibling) {
		t.Errorf("drew %s, which is only related by having the same parent: %v", sibling, nodeIDs(g))
	}
}

// TestHidingIsDrawnAsItsOwnKind. A blocked action is waiting; a hidden one is
// being deliberately not shown yet. Both stop it being actionable and only one
// of them is a dependency, so they must not be drawn alike.
func TestHidingIsDrawnAsItsOwnKind(t *testing.T) {
	db := initDB(t)
	first := addAction(t, db, "--title", "the one in front", "--verb", "write")
	behind := addAction(t, db, "--title", "tidy up after", "--verb", "write")
	if _, err := runCLI(t, "action", "hide-behind", "--db", db,
		"--action", behind, "--behind", first); err != nil {
		t.Fatalf("action hide-behind returned error: %v", err)
	}

	g := graphFor(t, db)
	if !hasEdge(g, first, behind, edgeHides) {
		t.Errorf("no hiding edge %s -> %s in %v", first, behind, g.Edges)
	}
	if hasEdge(g, first, behind, edgeBlocks) {
		t.Errorf("hiding was drawn as blocking, which says the wrong thing: %v", g.Edges)
	}
	if arrowFor(edgeHides) == arrowFor(edgeBlocks) {
		t.Error("hiding and blocking draw the same arrow")
	}
}

// TestTheDiagramSaysWhatItDoesNotKnow. A diagram implies completeness, and
// this one is a view of a few edge tables.
func TestTheDiagramSaysWhatItDoesNotKnow(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "one")
	blocked := addProject(t, db, "two")
	if _, err := runCLI(t, "project", "block", "--db", db,
		"--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	g := graphFor(t, db)
	if len(g.Coverage) == 0 {
		t.Fatal("the diagram states no coverage at all")
	}
	joined := strings.Join(g.Coverage, " ")
	for _, want := range []string{"blocking", "closed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("coverage does not mention %q: %v", want, g.Coverage)
		}
	}
}

// TestTheGraphPageAndTheIndexDrawTheSame. Two builders would eventually
// disagree, which is the argument the renderer is one function.
func TestTheGraphPageAndTheIndexDrawTheSame(t *testing.T) {
	db := renderedFixture(t)
	ctx := context.Background()

	st, err := store.OpenStore(db)
	if err != nil {
		t.Fatalf("OpenStore() returned error: %v", err)
	}
	defer st.Close()
	cfg, err := pageSettings(ctx, st)
	if err != nil {
		t.Fatalf("pageSettings() returned error: %v", err)
	}

	now := time.Now()
	index, err := buildRoute(ctx, st, now, false, cfg, route{kind: pageIndex})
	if err != nil {
		t.Fatalf("buildRoute(index) returned error: %v", err)
	}
	own, err := buildRoute(ctx, st, now, false, cfg, route{kind: pageGraph})
	if err != nil {
		t.Fatalf("buildRoute(graph) returned error: %v", err)
	}
	if index.Graph == nil || own.Graph == nil {
		t.Fatal("one of the two pages built no graph")
	}
	if index.Graph.Mermaid != own.Graph.Mermaid {
		t.Errorf("the index and its own page draw different diagrams:\n%s\n---\n%s",
			index.Graph.Mermaid, own.Graph.Mermaid)
	}
}

// TestAPageAboutOneThingBuildsNoGraph. It costs four queries and a listing of
// every pull request, and a page about one action has no use for it.
func TestAPageAboutOneThingBuildsNoGraph(t *testing.T) {
	db := renderedFixture(t)
	ctx := context.Background()

	st, err := store.OpenStore(db)
	if err != nil {
		t.Fatalf("OpenStore() returned error: %v", err)
	}
	defer st.Close()
	cfg, err := pageSettings(ctx, st)
	if err != nil {
		t.Fatalf("pageSettings() returned error: %v", err)
	}

	for _, kind := range []string{pageProjects, pageActions} {
		content, err := buildRoute(ctx, st, time.Now(), false, cfg, route{kind: kind})
		if err != nil {
			t.Fatalf("buildRoute(%s) returned error: %v", kind, err)
		}
		if content.Graph != nil {
			t.Errorf("the %s page built a graph it does not draw", kind)
		}
	}
}

// TestAnActionAdvancesItsProjectOnce is the arrow that was drawn twice. An
// action reached by blocking names the project it advances, and the project
// then enumerates its open actions and names the same edge coming back — so
// every action seeded by a dependency had two arrows to its project.
func TestAnActionAdvancesItsProjectOnce(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "cel2sql generates invalid SQLite")
	first := addAction(t, db, "--title", "wait for upstream", "--verb", "write",
		"--project", project)
	second := addAction(t, db, "--title", "update the dependency", "--verb", "write",
		"--project", project)
	if _, err := runCLI(t, "action", "add-blocker", "--db", db,
		"--from", second, "--to", first); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}

	g := graphFor(t, db)
	for _, action := range []string{first, second} {
		var n int
		for _, e := range g.Edges {
			if e.From == action && e.To == project && e.Kind == edgeAdvances {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s advances %s %d times, want once", action, project, n)
		}
	}

	// Stated once more as the invariant, so a second path to the same edge is
	// caught wherever it is added rather than only for this shape.
	seen := map[graphEdge]bool{}
	for _, e := range g.Edges {
		if seen[e] {
			t.Errorf("edge drawn twice: %+v", e)
		}
		seen[e] = true
	}
}

// TestTheLegendSaysOnlyWhatWasDrawn. A legend listing eight states beside a
// diagram using two is a second thing to read before the first makes sense.
func TestTheLegendSaysOnlyWhatWasDrawn(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "one")
	first := addAction(t, db, "--title", "a", "--verb", "write", "--project", project)
	second := addAction(t, db, "--title", "b", "--verb", "write", "--project", project)
	if _, err := runCLI(t, "action", "add-blocker", "--db", db,
		"--from", second, "--to", first); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}

	g := graphFor(t, db)
	legend := g.Legend
	if legend.Empty() {
		t.Fatal("a drawn diagram has an empty legend")
	}

	// Every entry names something the diagram used.
	states, kinds, shapes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, n := range g.Nodes {
		states[className(n.Class)] = true
		shapes[n.Kind] = true
	}
	for _, e := range g.Edges {
		kinds[e.Kind] = true
	}
	for _, e := range legend.States {
		if !states[e.Key] {
			t.Errorf("legend explains state %q, which nothing is drawn in", e.Key)
		}
	}
	for _, e := range legend.Shapes {
		if !shapes[e.Key] {
			t.Errorf("legend explains shape %q, which nothing is drawn as", e.Key)
		}
	}
	for _, e := range legend.Edges {
		if !kinds[e.Key] {
			t.Errorf("legend explains edge %q, which nothing is drawn with", e.Key)
		}
	}
	// And nothing the diagram used goes unexplained.
	if len(legend.States) != len(states) || len(legend.Edges) != len(kinds) ||
		len(legend.Shapes) != len(shapes) {
		t.Errorf("legend has %d/%d/%d entries for %d states, %d edge kinds, %d shapes",
			len(legend.States), len(legend.Edges), len(legend.Shapes),
			len(states), len(kinds), len(shapes))
	}
	// No pull request is drawn here, so the legend must not mention one.
	for _, e := range legend.Shapes {
		if e.Key == nodePR {
			t.Error("legend explains pull requests in a diagram with none")
		}
	}
}

// TestAnEmptyGraphHasNoLegend: there is nothing to explain, and a legend for a
// sentence is furniture.
func TestAnEmptyGraphHasNoLegend(t *testing.T) {
	db := initDB(t)
	addProject(t, db, "nothing blocked on anything")

	if g := graphFor(t, db); !g.Legend.Empty() {
		t.Errorf("an empty diagram has a legend: %+v", g.Legend)
	}
}

// TestAnActionIsJoinedToItsPullRequest is #88's largest follow-up: without it
// a stack of pull requests and the work that produced them were two
// disconnected pictures on the same page.
func TestAnActionIsJoinedToItsPullRequest(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/repo")
	for _, n := range []string{"1", "2"} {
		if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#"+n); err != nil {
			t.Fatalf("pr track returned error: %v", err)
		}
		if _, err := runCLI(t, "pr", "set", "--db", db, "owner/repo#"+n, "--pipeline", ""); err != nil {
			t.Fatalf("pr set returned error: %v", err)
		}
	}
	observePR(t, db, "owner/repo#1", "OPEN")

	first := addAction(t, db, "--title", "write it", "--verb", "write",
		"--pr", "owner/repo#1")
	second := addAction(t, db, "--title", "then this", "--verb", "write")
	if _, err := runCLI(t, "action", "add-blocker", "--db", db,
		"--from", second, "--to", first); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}

	g := graphFor(t, db)
	if !hasNode(g, "owner/repo#1") {
		t.Fatalf("the pull request the work is about is missing: %v", nodeIDs(g))
	}
	if !hasEdge(g, first, "owner/repo#1", edgeAbout) {
		t.Errorf("no about edge %s -> owner/repo#1 in %v", first, g.Edges)
	}
	// It is not a dependency, and must not be drawn as one: an action is
	// about a pull request and neither waits for the other.
	if arrowFor(edgeAbout) == arrowFor(edgeBlocks) {
		t.Error("being about a pull request draws the same line as blocking")
	}
	// And it seeds nothing: a pull request nothing in the graph is about
	// stays out, or the diagram grows a box per tracked pull request.
	if hasNode(g, "owner/repo#2") {
		t.Errorf("drew a pull request no action in the graph is about: %v", nodeIDs(g))
	}
}

// TestAMergedPullRequestIsNotDrawn. It is history in the way a satisfied
// blocker is: the action is still about it, and nothing is waiting on it.
func TestAMergedPullRequestIsNotDrawn(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/repo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	observePR(t, db, "owner/repo#1", "MERGED")

	first := addAction(t, db, "--title", "write it", "--verb", "write", "--pr", "owner/repo#1")
	second := addAction(t, db, "--title", "then this", "--verb", "write")
	if _, err := runCLI(t, "action", "add-blocker", "--db", db,
		"--from", second, "--to", first); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}

	if g := graphFor(t, db); hasNode(g, "owner/repo#1") {
		t.Errorf("drew a merged pull request: %v", nodeIDs(g))
	}
}

// observePR writes what GitHub would say, since state is observed and there is
// no command that sets it — which is the point of the actor rule.
func observePR(t *testing.T, db, key, state string) {
	t.Helper()
	ctx := context.Background()

	st, err := store.OpenStore(db)
	if err != nil {
		t.Fatalf("OpenStore() returned error: %v", err)
	}
	defer st.Close()

	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, key)
	if err != nil {
		t.Fatalf("LoadPR(%s) returned error: %v", key, err)
	}
	after := before.Clone()
	after.State = sql.NullString{String: state, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestTheQueueKeySaysOnlyWhatIsThere. The colours mean something specific and
// nothing on the page said what — but a key naming six bands beside a queue
// using two is a second thing to read before the first one makes sense.
func TestTheQueueKeySaysOnlyWhatIsThere(t *testing.T) {
	rows := []actionView{
		{ID: "NA1", RankClass: "session"},
		{ID: "NA2", RankClass: "decide"},
		{ID: "NA3", RankClass: "session"},
	}
	key := queueKey(rows)
	if len(key) != 2 {
		t.Fatalf("key = %v, want the two bands the rows use", key)
	}
	// In the order the ranking reads, not the order the rows happen to arrive.
	if key[0].Key != "decide" || key[1].Key != "session" {
		t.Errorf("key = %v, want decide before session", key)
	}

	// Late and expired are conditions rather than bands, so they appear only
	// when something is in them.
	for _, band := range key {
		if band.Key == "late" || band.Key == "expired" {
			t.Errorf("key explains %q with nothing in it", band.Key)
		}
	}
	withLate := queueKey([]actionView{{RankClass: "session", Late: "3 days"}})
	var sawLate bool
	for _, band := range withLate {
		sawLate = sawLate || band.Key == "late"
	}
	if !sawLate {
		t.Errorf("key = %v, want it to explain late when something is", withLate)
	}
}

// TestAnEmptyQueueHasNoKey: a key for a queue with nothing in it is furniture.
func TestAnEmptyQueueHasNoKey(t *testing.T) {
	if key := queueKey(nil); len(key) != 0 {
		t.Errorf("key = %v for an empty queue", key)
	}
}
