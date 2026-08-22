package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
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
	edgeBlocks = "blocks" // must finish first
	// edgeContains runs from the child to the parent, the way it is said: SL21
	// is part of SL108. Drawn parent-to-child it read as the parent depending
	// on its children, and put the umbrella above work that is meant to roll
	// up into it.
	edgeContains = "contains" // is part of
	edgeAdvances = "advances" // this action moves that project
	edgeHides    = "hides"    // folded out of the queue behind
	edgeStacks   = "stacks"   // this pull request is based on that one
	edgeAbout    = "about"    // this action is about that pull request
)

// dependencyGraph is what the diagram draws, after pruning.
type dependencyGraph struct {
	Nodes []graphNode
	Edges []graphEdge
	// Diagrams are the diagram sources, rendered by the client, one per disjoint
	// pile of work and in the order to stack them down the page.
	Diagrams []string
	// Links is where each node goes when clicked, keyed by the identifier the
	// diagram source uses, for the client to bind after it draws.
	Links template.JS
	// Coverage says what the diagram does and does not know, in prose, so the
	// page can state it. A diagram implies completeness, and this one is not
	// complete — see #88.
	Coverage []string
	// Omitted counts what pruning left out, so "small" and "broken" are
	// distinguishable at a glance.
	OmittedProjects int
	OmittedActions  int
	// Legend says what the colours and lines mean, for the ones used.
	Legend graphLegend
}

// Empty reports whether there is nothing to draw, which is a real answer
// rather than a failure: a queue with no recorded dependencies has no
// dependency graph.
func (g *dependencyGraph) Empty() bool { return len(g.Nodes) == 0 }

type graphNode struct {
	ID    string
	Kind  string
	Label string
	// Ready marks what could be picked up now: for an action, that it is in
	// the queue; for a pull request, that the step it is on is. The state
	// classes cannot say this -- a merge is coloured one-click whether or not
	// anything is in front of it.
	Ready bool
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
	projects []*store.Project, actions []*store.Action, verbs []*store.ActionVerb,
	repoShort map[string]string, late map[string]int) (*dependencyGraph, error) {

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
	subjects, err := st.SubjectPREdges(ctx)
	if err != nil {
		return nil, err
	}
	prs, err := st.ListPRs(ctx, store.PRFilter{})
	if err != nil {
		return nil, err
	}
	// What could be picked up right now. Asked of the store with the filter the
	// queue itself uses rather than worked out from state here: "ready" alone
	// is not the answer -- an action folded behind another, or one whose
	// project is blocked, is ready and still not something to do -- and two
	// definitions of the queue would eventually disagree with each other.
	unblocked, err := st.ListActions(ctx, store.ActionFilter{Unblocked: true})
	if err != nil {
		return nil, err
	}
	actionable := make(map[string]bool, len(unblocked))
	for _, a := range unblocked {
		actionable[a.ID] = true
	}

	rank := map[string]string{}
	prVerb := map[string]bool{}
	for _, v := range verbs {
		rank[v.Verb] = v.RankClass
		// A verb whose predicate is a fact about a pull request is a step in
		// getting one merged. Read from the predicate rather than from a list
		// of verb names, so a pipeline step added later folds without anyone
		// remembering to add it here -- and so wait_ref and wait_issue, which
		// are predicate verbs about something else entirely, do not.
		prVerb[v.Verb] = strings.HasPrefix(v.PredicateKey.String, "pr_")
	}

	g := &graphBuilder{
		projects:   byID(projects, func(p *store.Project) string { return p.ID }),
		actions:    byID(actions, func(a *store.Action) string { return a.ID }),
		prs:        byID(prs, func(p *store.PR) string { return p.ID }),
		rank:       rank,
		prVerb:     prVerb,
		repoShort:  repoShort,
		actionable: actionable,
		late:       late,
		seen:       map[string]bool{},
		drawn:      map[graphEdge]bool{},
		collapsed:  map[string]string{},
		foldedInto: map[string][]string{},
	}
	g.fold(subjects)
	return g.build(projectBlocks, actionBlocks, hidden, stacked, subjects), nil
}

func byID[T any](rows []T, id func(T) string) map[string]T {
	m := make(map[string]T, len(rows))
	for _, row := range rows {
		m[id(row)] = row
	}
	return m
}

type graphBuilder struct {
	projects   map[string]*store.Project
	actions    map[string]*store.Action
	prs        map[string]*store.PR
	rank       map[string]string
	prVerb     map[string]bool
	repoShort  map[string]string
	actionable map[string]bool
	late       map[string]int

	// collapsed is the action that is not drawn and the pull request it is
	// drawn as; foldedInto is the same thing read the other way, in the order
	// the steps were created.
	collapsed  map[string]string
	foldedInto map[string][]string

	seen  map[string]bool
	drawn map[graphEdge]bool
	nodes []graphNode
	edges []graphEdge
}

// fold decides which actions are drawn as the pull request they are about
// rather than as themselves.
//
// A pipeline is four steps and they are the same four every time: un-draft,
// announce, wait for review, merge. Drawn literally that is four boxes, three
// arrows between them, four more to the project and four to the pull request
// they are all about — eleven marks to say a thing the reader already knows
// the shape of, per pull request. On the queue this graph was drawn from, 25
// of 37 action nodes were pipeline steps, and 69 of 103 edges touched one. The
// twelve that remained are the decisions and the writing: exactly what a
// picture of "what is holding this up" should be made of.
//
// What survives the fold is where the pipeline has got to, which is the only
// part that differs between one pull request and the next: the frontmost open
// step gives the pull request its colour and its verb. So `NA218 wait_review →
// NA219 merge → cloud-terraform#1134` becomes `cloud-terraform#1134
// (wait_review)`, coloured as a wait.
//
// Only pipeline steps fold. An action that is about a pull request but is real
// work — write the thing, decide the approach — stays its own box, because
// what it says is not recoverable from the pull request's state.
func (g *graphBuilder) fold(subjects []store.Edge) {
	for _, e := range subjects {
		a, ok := g.actions[e.From]
		if !ok || !a.IsOpen() || !g.prVerb[a.Verb] || !g.prOpen(e.To) {
			continue
		}
		g.collapsed[e.From] = e.To
		g.foldedInto[e.To] = append(g.foldedInto[e.To], e.From)
	}
	for pr := range g.foldedInto {
		sort.Slice(g.foldedInto[pr], func(i, j int) bool {
			return g.actions[g.foldedInto[pr][i]].N < g.actions[g.foldedInto[pr][j]].N
		})
	}
}

// prName is what a box calls a pull request: `ip#4204` where the repository
// has a short name, and the full `owner/repo#4204` where it does not.
//
// The short name is what the person writing the queue already types, and here
// it buys room a diagram has none of: the long form is most of a node's width
// spent on the same eleven characters as its neighbour. The identifier stays
// the full one -- this is the label, and nothing keys off it.
func (g *graphBuilder) prName(p *store.PR) string {
	if short, ok := g.repoShort[p.Repo]; ok && short != "" {
		return fmt.Sprintf("%s#%d", short, p.Number)
	}
	return p.ID
}

// resolve is the folded action's stand-in. Everything that adds a node or an
// edge goes through it, so folding is one decision applied in one place rather
// than a condition at every call site.
func (g *graphBuilder) resolve(id string) string {
	if pr, ok := g.collapsed[id]; ok {
		return pr
	}
	return id
}

func (g *graphBuilder) build(projectBlocks, actionBlocks, hidden, stacked, subjects []store.Edge) *dependencyGraph {
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
	// A pull request stands in for the steps folded into it, so it inherits
	// what they advanced. Without this a stack reached by stacking alone hangs
	// off nothing, and the project it is the work for is somewhere else on the
	// page.
	for _, id := range g.currentIDs(nodePR) {
		for _, aid := range g.foldedInto[id] {
			a := g.actions[aid]
			if a.ProjectID.Valid && g.projectOpen(a.ProjectID.String) {
				g.addProject(a.ProjectID.String)
				g.addEdge(id, a.ProjectID.String, edgeAdvances)
			}
		}
	}

	// The pull request an action is about, which is what joins the two halves
	// of the diagram. Without it a stack of pull requests and the work that
	// produced them are two disconnected pictures on the same page.
	//
	// It seeds nothing: a PR reaches the graph by being stacked, or by an
	// action already in it being about one. Starting from every subject link
	// would draw a box for every tracked pull request, which is a hundred and
	// twenty of them.
	//
	// Open only. A merged pull request is history in the way a closed blocker
	// is: the action is still about it, and nothing is waiting on it.
	for _, e := range subjects {
		if !g.seen[e.From] || !g.prOpen(e.To) {
			continue
		}
		g.addPR(e.To)
		g.addEdge(e.From, e.To, edgeAbout)
	}

	// Containment, one level up. It seeds nothing on its own: a parent is
	// context for a dependency, not a dependency, and starting from it would
	// draw every project that happens to be part of something.
	//
	// And only where it groups. A parent is worth a box when it says that
	// several of these are the same piece of work — six blocked siblings
	// reading as one thing is the whole reason it is drawn. A parent with one
	// child in the diagram says only what that child's own page says, and
	// costs a node, an edge, and the distance the layout puts between them.
	children := map[string][]string{}
	for _, id := range g.currentIDs(nodeProject) {
		p := g.projects[id]
		if p.ParentID.Valid && g.projectOpen(p.ParentID.String) {
			children[p.ParentID.String] = append(children[p.ParentID.String], id)
		}
	}
	for _, parent := range sortedKeys(children) {
		if len(children[parent]) < 2 {
			continue
		}
		g.addProject(parent)
		for _, id := range children[parent] {
			g.addEdge(id, parent, edgeContains)
		}
	}

	graph := &dependencyGraph{Nodes: g.nodes, Edges: g.edges}
	graph.OmittedProjects = countOpen(g.projects, func(p *store.Project) bool {
		return p.IsOpen() && !g.seen[p.ID]
	})
	graph.OmittedActions = countOpen(g.actions, func(a *store.Action) bool {
		return a.IsOpen() && !g.seen[a.ID]
	})
	// Diagrams before coverage, which says how many of them there are and why.
	graph.Diagrams = mermaid(graph)
	graph.Coverage = coverage(graph)
	graph.Legend = legendFor(graph)
	// Marshalling cannot fail on a map of strings, and a diagram that drew is
	// worth more than one that refused over its navigation, so an error here
	// costs the links and nothing else.
	if encoded, err := json.Marshal(mermaidLinks(graph)); err == nil {
		graph.Links = template.JS(encoded)
	}
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
	if pr, folded := g.collapsed[id]; folded {
		g.addPR(pr)
		return
	}
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
		Ready: g.actionable[id],
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

	// A pull request with a pipeline folded into it says where that pipeline
	// has got to; one without says only that it is open, which is what its
	// state already said.
	note, class, ready := "", strings.ToLower(p.State.String), false
	if folded := g.foldedInto[id]; len(folded) > 0 {
		front := g.actions[folded[0]]
		note, class = front.Verb, g.rank[front.Verb]
		// The step it is on, not the pull request: sc#3177's un-draft is folded
		// behind NA95, so the pull request is not something to do even though
		// un-drafting is one click.
		ready = g.actionable[front.ID]
		if _, isLate := g.late[front.ID]; isLate {
			class = "late"
		}
		for _, aid := range folded {
			g.seen[aid] = true // drawn, as part of this; not omitted
		}
	}
	g.nodes = append(g.nodes, graphNode{
		ID:    id,
		Kind:  nodePR,
		Label: nodeLabel(g.prName(p), p.Title, note),
		Ready: ready,
		Class: class,
		Href:  p.URL.String,
	})
}

// addEdge records one edge once.
//
// The dedupe is load-bearing rather than defensive. An action reached by
// blocking names the project it advances, and the project then enumerates its
// open actions and names the same edge coming back — so every action that was
// seeded by a dependency drew its advances edge twice, which mermaid renders
// as two arrows between the same pair of boxes. Keyed on the whole edge, so two
// kinds between one pair still draw as two.
func (g *graphBuilder) addEdge(from, to, kind string) {
	// Both ends through the fold, which is also what drops a pipeline's
	// internal edges: wait-review blocks merge becomes the pull request
	// blocking itself, and an edge from a thing to itself is not drawn.
	from, to = g.resolve(from), g.resolve(to)
	if from == to {
		return
	}
	e := graphEdge{From: from, To: to, Kind: kind}
	if g.drawn[e] {
		return
	}
	g.drawn[e] = true
	g.edges = append(g.edges, e)
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
	said = append(said, "the open pull request an action is about, which joins the two")
	said = append(said,
		"a pull request's pipeline steps drawn as the pull request, labelled with "+
			"the step it is on, rather than as a box each")
	said = append(said,
		"nothing closed: a satisfied blocker is history, and it is left out")
	// Said explicitly, because several pictures under one heading otherwise
	// read as one picture that failed to join up. Nothing crosses between them
	// by construction: they are the graph's connected components.
	if len(g.Diagrams) > 1 {
		line := fmt.Sprintf(
			"%d diagrams, one per pile of work with nothing joining it to the others",
			len(g.Diagrams))
		// The last one is the exception and holds several piles, so it would
		// contradict the sentence above if left unsaid.
		for _, group := range mermaidComponents(g) {
			if len(group) < ownPanel {
				line += "; the last gathers the piles too small to draw alone"
				break
			}
		}
		said = append(said, line)
	}
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

// graphLegend says what the diagram's colours and lines mean.
//
// Built from what was drawn rather than from the full vocabulary. A legend
// listing eight states beside a diagram using two is a second thing to read
// before the first one makes sense, and the states a queue happens to be in
// change from day to day.
type graphLegend struct {
	// States are the node colours, Shapes the node outlines, Edges the lines.
	States []legendEntry
	Shapes []legendEntry
	Edges  []legendEntry
	// Ready says whether anything is drawn as ready, and so whether the page
	// should explain what the strong and faded boxes mean. A key to a
	// distinction the picture is not making is a thing to read for nothing.
	Ready bool
}

// legendEntry is one swatch. Key is the CSS class the stylesheet draws it
// with, which is the same class the diagram itself carries — so a legend that
// disagrees with the picture is a stylesheet bug rather than a drift between
// two lists.
type legendEntry struct {
	Key   string
	Label string
}

func (l graphLegend) Empty() bool {
	return len(l.States) == 0 && len(l.Shapes) == 0 && len(l.Edges) == 0 && !l.Ready
}

// The glosses, in the order a legend reads. Slices rather than maps: the order
// is part of the answer, and ranging a map would reshuffle the legend on every
// request for a diagram that had not changed.
var (
	stateLabels = []legendEntry{
		{"active", "active"},
		{"blocked", "blocked"},
		{"snoozed", "snoozed"},
		{"click", "one click"},
		{"decide", "a judgement"},
		{"session", "real work"},
		{"wait", "waiting on somebody"},
		{"late", "past its allowance"},
		{"open", "open"},
		{"plain", "no state worth colouring"},
	}
	shapeLabels = []legendEntry{
		{nodeProject, "project"},
		{nodeAction, "action"},
		{nodePR, "pull request"},
	}
	edgeLabels = []legendEntry{
		{edgeBlocks, "must finish first"},
		{edgeAdvances, "advances it"},
		{edgeContains, "is part of"},
		{edgeHides, "folded behind"},
		{edgeStacks, "stacked on"},
		{edgeAbout, "is about"},
	}
)

// legendFor keeps the entries the diagram used, in the order above.
func legendFor(g *dependencyGraph) graphLegend {
	states, shapes, kinds := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, n := range g.Nodes {
		states[className(n.Class)] = true
		shapes[n.Kind] = true
	}
	for _, e := range g.Edges {
		kinds[e.Kind] = true
	}
	var ready bool
	for _, n := range g.Nodes {
		if n.Ready {
			ready = true
			break
		}
	}
	return graphLegend{
		States: used(stateLabels, states),
		Shapes: used(shapeLabels, shapes),
		Edges:  used(edgeLabels, kinds),
		Ready:  ready,
	}
}

func used(all []legendEntry, present map[string]bool) []legendEntry {
	var kept []legendEntry
	for _, e := range all {
		if present[e.Key] {
			kept = append(kept, e)
		}
	}
	return kept
}

// The bands a queue is read in. Order is the one the ranking uses, so the key
// reads down in the same direction the list does.
var queueBands = []legendEntry{
	{"click", "one click"},
	{"decide", "a judgement"},
	{"session", "real work"},
	{"wait", "waiting on somebody"},
	{"late", "past its allowance"},
	{"expired", "snoozed past its date"},
}

// queueKey is the legend the queue never had. The colours mean something
// specific and nothing on the page said what.
//
// Built from the rows actually drawn, for the reason the diagram's legend is:
// a key naming six bands beside a queue using two is a second thing to read
// before the first one makes sense. Late and expired are conditions rather
// than bands — an overdue `decide` is still a judgement — so they appear only
// when something is in them.
func queueKey(rows ...[]actionView) []legendEntry {
	present := map[string]bool{}
	for _, group := range rows {
		for _, a := range group {
			present[a.RankClass] = true
			if a.Late != "" {
				present["late"] = true
			}
			if a.Expired {
				present["expired"] = true
			}
		}
	}
	return used(queueBands, present)
}
