package cli

import (
	"context"
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
