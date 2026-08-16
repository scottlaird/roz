package cli

import (
	"context"
	"sort"
	"strings"

	"github.com/scottlaird/roz/internal/store"
)

// The kinds of node a dependency diagram draws. Shape is chosen from these,
// so a reader can tell a project from the work advancing it without reading
// the identifier.
const (
	nodeProject = "project"
	nodeAction  = "action"
	nodePR      = "pr"
)

// The kinds of edge. They mean different things and must not be drawn alike:
// blocking is a dependency, containment is a statement about what a piece of
// work is made of, and neither implies the other.
const (
	edgeBlocks   = "blocks"   // must finish first
	edgeContains = "contains" // is part of
	edgeAdvances = "advances" // this action moves that project
	edgeHides    = "hides"    // folded out of the queue behind
	edgeStacks   = "stacks"   // this pull request is based on that one
)

// dependencyGraph is what the diagram draws, after pruning.
type dependencyGraph struct {
	Nodes []graphNode
	Edges []graphEdge
	// Mermaid is the diagram source, rendered by the client.
	Mermaid string
	// Coverage says what the diagram does and does not know, in prose, so the
	// page can state it. A diagram implies completeness, and this one is not
	// complete — see #88.
	Coverage []string
	// Omitted counts what pruning left out, so "small" and "broken" are
	// distinguishable at a glance.
	OmittedProjects int
	OmittedActions  int
}

// Empty reports whether there is nothing to draw, which is a real answer
// rather than a failure: a queue with no recorded dependencies has no
// dependency graph.
func (g *dependencyGraph) Empty() bool { return len(g.Nodes) == 0 }

type graphNode struct {
	ID    string
	Kind  string
	Label string
	// Class is the state worth colouring by: a project's status, an action's
	// rank class, a pull request's state.
	Class string
	Href  string
}

type graphEdge struct {
	From string
	To   string
	Kind string
}

// buildGraph reads the recorded dependencies and prunes them to what is worth
// drawing.
//
// # What gets in, and why so little does
//
// The pruning is the whole design. Every open project and action drawn at once
// is sixty-odd boxes with three arrows between them, which is strictly worse
// than the list it sits under — the list at least has an order. So a node
// earns its place by being part of a dependency, and nothing else does:
//
//   - A project is seeded by a blocking edge whose other end is also open.
//   - An action is seeded the same way, by blocking or by being folded behind
//     another; a pull request by being stacked on another.
//   - Containment is not a dependency and seeds nothing. It is drawn between
//     nodes already present, and pulls in a parent one level so a group of
//     blocked siblings reads as the thing they are part of.
//   - A seeded project pulls in its open actions, and a seeded action pulls in
//     its project. That is the layer the queue cannot show: which piece of work
//     is actually holding the dependency up.
//
// Closed ends are dropped rather than drawn faded. A satisfied blocker is
// history — the edge stays in the database for ever, since nothing deletes one
// — and a diagram that draws every finished chain answers "how did we get
// here" at the cost of never answering "what now". The counts of what was left
// out are reported instead, because a diagram that is small for a good reason
// and one that is broken look identical.
func buildGraph(ctx context.Context, st *store.Store,
	projects []*store.Project, actions []*store.Action, rank map[string]string,
	late map[string]int) (*dependencyGraph, error) {

	projectBlocks, err := st.ProjectBlockEdges(ctx)
	if err != nil {
		return nil, err
	}
	actionBlocks, err := st.ActionBlockEdges(ctx)
	if err != nil {
		return nil, err
	}
	hidden, err := st.HiddenBehindEdges(ctx)
	if err != nil {
		return nil, err
	}
	stacked, err := st.StackedOnEdges(ctx)
	if err != nil {
		return nil, err
	}
	prs, err := st.ListPRs(ctx, store.PRFilter{})
	if err != nil {
		return nil, err
	}

	g := &graphBuilder{
		projects: byID(projects, func(p *store.Project) string { return p.ID }),
		actions:  byID(actions, func(a *store.Action) string { return a.ID }),
		prs:      byID(prs, func(p *store.PR) string { return p.ID }),
		rank:     rank,
		late:     late,
		seen:     map[string]bool{},
	}
	return g.build(projectBlocks, actionBlocks, hidden, stacked), nil
}

func byID[T any](rows []T, id func(T) string) map[string]T {
	m := make(map[string]T, len(rows))
	for _, row := range rows {
		m[id(row)] = row
	}
	return m
}

type graphBuilder struct {
	projects map[string]*store.Project
	actions  map[string]*store.Action
	prs      map[string]*store.PR
	rank     map[string]string
	late     map[string]int

	seen  map[string]bool
	nodes []graphNode
	edges []graphEdge
}

func (g *graphBuilder) build(projectBlocks, actionBlocks, hidden, stacked []store.Edge) *dependencyGraph {
	// Seeding. Only edges with both ends open, which is what makes the
	// diagram about now rather than about everything that ever blocked
	// anything.
	for _, e := range projectBlocks {
		if g.projectOpen(e.From) && g.projectOpen(e.To) {
			g.addProject(e.From)
			g.addProject(e.To)
			g.addEdge(e.From, e.To, edgeBlocks)
		}
	}
	for _, e := range actionBlocks {
		if g.actionOpen(e.From) && g.actionOpen(e.To) {
			g.addAction(e.From)
			g.addAction(e.To)
			g.addEdge(e.From, e.To, edgeBlocks)
		}
	}
	for _, e := range hidden {
		if g.actionOpen(e.From) && g.actionOpen(e.To) {
			g.addAction(e.From)
			g.addAction(e.To)
			g.addEdge(e.From, e.To, edgeHides)
		}
	}
	for _, e := range stacked {
		if g.prOpen(e.From) && g.prOpen(e.To) {
			g.addPR(e.From)
			g.addPR(e.To)
			g.addEdge(e.From, e.To, edgeStacks)
		}
	}

	// The work layer. A seeded action names the project it advances, and a
	// seeded project names the work still open against it — which is the
	// thing the flat list cannot show, since it has no room to say that the
	// dependency is really about one write step nobody has done.
	//
	// Snapshotted first: adding to it while ranging over it is how a graph
	// grows a hop at a time until it is the whole database again.
	for _, id := range g.currentIDs(nodeAction) {
		a := g.actions[id]
		if a.ProjectID.Valid && g.projectOpen(a.ProjectID.String) {
			g.addProject(a.ProjectID.String)
			g.addEdge(id, a.ProjectID.String, edgeAdvances)
		}
	}
	for _, id := range g.currentIDs(nodeProject) {
		for _, a := range g.openActionsOf(id) {
			g.addAction(a.ID)
			g.addEdge(a.ID, id, edgeAdvances)
		}
	}

	// Containment, one level up. It seeds nothing on its own: a parent is
	// context for a dependency, not a dependency, and starting from it would
	// draw every project that happens to be part of something.
	for _, id := range g.currentIDs(nodeProject) {
		p := g.projects[id]
		if p.ParentID.Valid && g.projectOpen(p.ParentID.String) {
			g.addProject(p.ParentID.String)
			g.addEdge(p.ParentID.String, id, edgeContains)
		}
	}

	graph := &dependencyGraph{Nodes: g.nodes, Edges: g.edges}
	graph.OmittedProjects = countOpen(g.projects, func(p *store.Project) bool {
		return p.IsOpen() && !g.seen[p.ID]
	})
	graph.OmittedActions = countOpen(g.actions, func(a *store.Action) bool {
		return a.IsOpen() && !g.seen[a.ID]
	})
	graph.Coverage = coverage(graph)
	graph.Mermaid = mermaid(graph)
	return graph
}

// currentIDs is the nodes of a kind as they stand, copied so that adding more
// while walking them cannot widen the walk.
func (g *graphBuilder) currentIDs(kind string) []string {
	var ids []string
	for _, n := range g.nodes {
		if n.Kind == kind {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

func (g *graphBuilder) openActionsOf(projectID string) []*store.Action {
	var found []*store.Action
	for _, a := range g.actions {
		if a.IsOpen() && a.ProjectID.Valid && a.ProjectID.String == projectID {
			found = append(found, a)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].N < found[j].N })
	return found
}

func (g *graphBuilder) projectOpen(id string) bool {
	p, ok := g.projects[id]
	return ok && p.IsOpen()
}

func (g *graphBuilder) actionOpen(id string) bool {
	a, ok := g.actions[id]
	return ok && a.IsOpen()
}

// prOpen is state rather than a closed_at: a pull request is done when GitHub
// says it is, and a merged one holds nothing up.
func (g *graphBuilder) prOpen(id string) bool {
	p, ok := g.prs[id]
	return ok && p.State.String == store.PRStateOpen
}

func (g *graphBuilder) addProject(id string) {
	if g.seen[id] {
		return
	}
	p := g.projects[id]
	g.seen[id] = true
	g.nodes = append(g.nodes, graphNode{
		ID:    id,
		Kind:  nodeProject,
		Label: nodeLabel(id, p.Title, nullIntText(p.Priority)),
		Class: p.Status,
		Href:  projectHref(id),
	})
}

func (g *graphBuilder) addAction(id string) {
	if g.seen[id] {
		return
	}
	a := g.actions[id]
	g.seen[id] = true
	class := g.rank[a.Verb]
	if _, isLate := g.late[id]; isLate {
		class = "late"
	}
	g.nodes = append(g.nodes, graphNode{
		ID:    id,
		Kind:  nodeAction,
		Label: nodeLabel(id, a.Title, a.Verb),
		Class: class,
		Href:  actionHref(id),
	})
}

func (g *graphBuilder) addPR(id string) {
	if g.seen[id] {
		return
	}
	p := g.prs[id]
	g.seen[id] = true
	g.nodes = append(g.nodes, graphNode{
		ID:    id,
		Kind:  nodePR,
		Label: nodeLabel(id, p.Title, ""),
		Class: strings.ToLower(p.State.String),
	})
}

func (g *graphBuilder) addEdge(from, to, kind string) {
	g.edges = append(g.edges, graphEdge{From: from, To: to, Kind: kind})
}

func countOpen[T any](rows map[string]T, keep func(T) bool) int {
	var n int
	for _, row := range rows {
		if keep(row) {
			n++
		}
	}
	return n
}

// nodeLabelWidth is how much of a title survives. A diagram is read by
// scanning it, so a node is an identifier and enough words to recognise it by;
// the full title is one click away on the page it links to.
const nodeLabelWidth = 38

// nodeLabel is what a box says: the identifier, a trimmed title, and the one
// annotation that changes what the node means — a project's priority, an
// action's verb.
func nodeLabel(id, title, note string) string {
	title = strings.Join(strings.Fields(title), " ")
	if len(title) > nodeLabelWidth {
		title = strings.TrimRight(title[:nodeLabelWidth-1], " ") + "…"
	}
	label := id
	if title != "" {
		label += " " + title
	}
	if note != "" && note != "-" {
		label += " (" + note + ")"
	}
	return label
}

// coverage states what the diagram is drawn from, because a diagram implies
// completeness and this one is not complete.
//
// Written from what was actually drawn rather than from a fixed list: a page
// that always claims to show pull-request stacks when none are open is making
// the same promise the silence would.
func coverage(g *dependencyGraph) []string {
	said := []string{
		"blocking between projects, and between actions",
		"the project each action advances, and what a project is part of",
	}
	if countEdges(g, edgeStacks) > 0 {
		said = append(said, "pull requests stacked on one another")
	} else {
		said = append(said, "pull-request stacking, where any is open (none is now)")
	}
	said = append(said,
		"nothing closed: a satisfied blocker is history, and it is left out")
	return said
}

func countEdges(g *dependencyGraph, kind string) int {
	var n int
	for _, e := range g.Edges {
		if e.Kind == kind {
			n++
		}
	}
	return n
}
