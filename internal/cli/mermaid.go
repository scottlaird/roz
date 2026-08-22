package cli

import (
	"fmt"
	"sort"
	"strings"
)

// mermaid renders the graph as mermaid flowchart source: one diagram per
// disjoint pile of work, in the order the page should stack them.
//
// Text, produced on the server and drawn by the client. The layout is the part
// worth having and the part nothing here wants to write: a dependency diagram
// needs a layered layout with edge routing, and that is a solved problem
// belonging to whatever draws it.
//
// Top down, so the arrows read as time down a page that is already read that
// way: a blocker is above what it blocks. Left to right lays a wide graph out
// along its longest axis and needs horizontal scrolling to follow one chain,
// which is the direction a page has least of.
//
// Split because one diagram is the wrong picture of unrelated work. Every
// layered engine puts each component's roots on the top rank and lays the
// components out side by side, so a graph of n piles is n times as wide as the
// widest thing in it and every pile starts level with the others -- 2190x629
// measured, against a 960px column. The same nodes as three diagrams came to
// 759, 664 and 848 wide, all of which fit, because stacking them spends the
// axis the page has rather than the one it does not.
func mermaid(g *dependencyGraph) []string {
	if g.Empty() {
		return nil
	}
	// One id map for the whole graph, not one per diagram. The client binds
	// navigation by the drawn node's id, from a single map keyed the same way,
	// and per-diagram numbering would collide across panels.
	ids := mermaidIDs(g.Nodes)
	var out []string
	for _, panel := range mermaidPanels(g) {
		out = append(out, mermaidDiagram(g, panel, ids))
	}
	return out
}

// mermaidDiagram renders one diagram, holding the nodes in panel and whatever
// edges run between them.
func mermaidDiagram(g *dependencyGraph, panel map[string]bool, ids map[string]string) string {
	var b strings.Builder
	b.WriteString("graph TD\n")

	// No classDef here. Mermaid's own grammar cannot parse a CSS var() inside
	// one — the bracket ends the token — so a palette written into the diagram
	// source would have to be literal colours, and literal colours are a
	// stylesheet that does not follow the page into dark mode. The class name
	// reaches the SVG either way, so roz.css styles it like anything else.
	for _, n := range g.Nodes {
		if !panel[n.ID] {
			continue
		}
		fmt.Fprintf(&b, "  %s%s:::%s\n",
			ids[n.ID], shapeFor(n.Kind, mermaidLabel(n.Label)), mermaidClass(n.Class))
	}
	for _, e := range g.Edges {
		if !panel[e.From] || !panel[e.To] {
			continue
		}
		from, to := ids[e.From], ids[e.To]
		if from == "" || to == "" {
			continue // an edge to something pruning dropped; the node is the record
		}
		fmt.Fprintf(&b, "  %s %s %s\n", from, arrowFor(e.Kind), to)
	}
	// The kind, as a second class, so the stylesheet can draw a project like a
	// project whatever state it is in. `:::` carries one class and the state
	// has it, since that is the one that varies; a `class` statement is how a
	// node gets another. Grouped into one statement per kind rather than one
	// per node, which is the same thing to mermaid and a great deal less of it
	// to read.
	for _, kind := range []string{nodeProject, nodeAction, nodePR} {
		var of []string
		for _, n := range g.Nodes {
			if panel[n.ID] && n.Kind == kind {
				of = append(of, ids[n.ID])
			}
		}
		if len(of) > 0 {
			fmt.Fprintf(&b, "  class %s %s\n", strings.Join(of, ","), kind)
		}
	}

	// And whether it can be picked up now, which is the question the diagram
	// is usually being asked. A third class rather than a colour of its own:
	// the state classes say what kind of work it is and are worth keeping, and
	// this cuts across all of them.
	var ready []string
	for _, n := range g.Nodes {
		if panel[n.ID] && n.Ready {
			ready = append(ready, ids[n.ID])
		}
	}
	if len(ready) > 0 {
		fmt.Fprintf(&b, "  class %s ready\n", strings.Join(ready, ","))
	}

	return b.String()
}

// ownPanel is how many nodes a pile needs before it is drawn on its own. Below
// it a component is a stub -- a project and the pull request advancing it --
// and a diagram of two boxes costs a bordered box, a scroll container and a
// screenful of page to say what one line of the queue already says. Stubs go
// into one shared panel, where they lay out in a row and cost a strip.
const ownPanel = 5

// mermaidPanels groups the graph into the diagrams to draw, largest first, with
// everything too small for its own diagram gathered into a last one.
//
// Size order rather than the node order, because the big piles are the reason
// the page has a diagram and should be the first thing under the heading. Ties
// keep the order the nodes came in, so the same data draws the same way.
func mermaidPanels(g *dependencyGraph) []map[string]bool {
	groups := mermaidComponents(g)
	var panels []map[string]bool
	stubs := map[string]bool{}
	for _, group := range groups {
		if len(group) >= ownPanel {
			panel := make(map[string]bool, len(group))
			for _, id := range group {
				panel[id] = true
			}
			panels = append(panels, panel)
			continue
		}
		for _, id := range group {
			stubs[id] = true
		}
	}
	if len(stubs) > 0 {
		panels = append(panels, stubs)
	}
	return panels
}

// mermaidComponents finds the connected components of the graph, ignoring which
// way the edges point: two nodes belong in the same picture if anything joins
// them at all.
//
// Returned largest first, each component in node order, and stable -- the
// diagram is rebuilt on every request, so an ordering that depended on map
// iteration would reshuffle the page under a reload that changed nothing.
func mermaidComponents(g *dependencyGraph) [][]string {
	parent := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		parent[n.ID] = n.ID
	}
	var root func(string) string
	root = func(id string) string {
		if parent[id] != id {
			parent[id] = root(parent[id])
		}
		return parent[id]
	}
	for _, e := range g.Edges {
		// An edge can outlive one of its ends, since pruning works on nodes.
		if _, ok := parent[e.From]; !ok {
			continue
		}
		if _, ok := parent[e.To]; !ok {
			continue
		}
		parent[root(e.From)] = root(e.To)
	}

	var order []string
	members := map[string][]string{}
	for _, n := range g.Nodes {
		r := root(n.ID)
		if _, seen := members[r]; !seen {
			order = append(order, r)
		}
		members[r] = append(members[r], n.ID)
	}
	groups := make([][]string, 0, len(order))
	for _, r := range order {
		groups = append(groups, members[r])
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return len(groups[i]) > len(groups[j])
	})
	return groups
}

// mermaidLinks is where each node goes when it is clicked, keyed by the
// identifier the diagram source uses.
//
// Not `click ... href` statements in the source, which is what this was.
// ELK renders nothing at all when the source carries them -- not an error, an
// empty diagram -- so the navigation is bound to the drawn nodes instead, from
// this. It also means the page no longer needs mermaid's "loose" security
// level, which existed only to let click statements navigate.
func mermaidLinks(g *dependencyGraph) map[string]string {
	ids := mermaidIDs(g.Nodes)
	links := map[string]string{}
	for _, n := range g.Nodes {
		if n.Href != "" {
			links[ids[n.ID]] = n.Href
		}
	}
	return links
}

// graphClasses are the states the stylesheet knows how to colour. Anything
// else becomes "plain" rather than reaching the page as a class name nothing
// styles, which would draw as an unmarked box and read as "no state" rather
// than as "a state nobody thought about".
var graphClasses = map[string]bool{
	"active": true, "blocked": true, "snoozed": true,
	"click": true, "decide": true, "session": true, "wait": true,
	"late": true, "open": true,
}

func className(class string) string {
	if graphClasses[class] {
		return class
	}
	return "plain"
}

// mermaidRenames are the classes that cannot reach the diagram under the name
// the rest of the page uses them by.
//
// "click" is a keyword in mermaid's own flowchart grammar -- it is how the
// navigation at the bottom of this file is written -- so `:::click` lexes as
// the keyword and the parse fails at the first node in that band, taking the
// whole diagram with it rather than the one node. Every other class here
// parses; this is the only collision.
//
// Renamed here rather than in the store, because the name is a rank class
// under a CHECK constraint and is shared with `ol.queue li.click` and the
// legend swatch. A migration to work around another parser's keyword list
// would be the wrong thing renaming. roz.css carries the graph-only selector
// under this name; the queue and the legend keep theirs.
var mermaidRenames = map[string]string{
	"click": "oneclick",
}

func mermaidClass(class string) string {
	name := className(class)
	if renamed, ok := mermaidRenames[name]; ok {
		return renamed
	}
	return name
}

// shapeFor distinguishes the layers without a legend: a project is a box, an
// action is rounded, a pull request is a stadium.
func shapeFor(kind, label string) string {
	switch kind {
	case nodeAction:
		return "(\"" + label + "\")"
	case nodePR:
		return "([\"" + label + "\"])"
	default:
		return "[\"" + label + "\"]"
	}
}

// arrowFor draws the kinds of edge differently, because they mean different
// things. A solid arrow is a dependency; containment is a dotted line with no
// arrowhead, since a parent does not block a child and drawing it the same way
// would say it does.
func arrowFor(kind string) string {
	switch kind {
	case edgeContains, edgeAbout:
		return "-.-"
	case edgeAdvances:
		return "-->"
	case edgeHides:
		return "-.->"
	default:
		return "==>"
	}
}

// mermaidLabel makes a title safe inside a quoted mermaid label.
//
// Everything here was checked against mermaid in a browser rather than reasoned
// about, because the rules are not what the syntax suggests and the failures
// are silent. With htmlLabels off — which is what keeps a title from becoming
// markup — a label is SVG text, and mermaid does not decode into it.
//
//   - A bare # is fine. The entity code #35; that mermaid's own documentation
//     gives for it renders as the literal text "&#35;", because the decode
//     that would turn it back happens only on the html-label path. Escaping it
//     is what put "cel2sql&#35;168" on the page.
//   - A double quote ends the label and cannot be escaped back: #quot; renders
//     as literal "&quot;", and a backslash renders as a backslash. So it
//     becomes a typographic quote, which is lossy and legible, and is the only
//     one of these that changes a character rather than leaving it alone.
//   - Backticks are removed rather than kept. A pair of them delimits a
//     markdown string, and mermaid drops what is between: "refuse
//     `superseded_by` now" renders as "refuse now". Losing the word silently is
//     worse than losing the quoting, and roz titles are full of backticks.
//   - < > and & are left exactly as they are, and render as "&lt;", "&gt;" and
//     "&amp;". Passing the entity form instead produces the same output, so
//     there is nothing to choose between them; the only fix would be to
//     substitute a character the title does not contain, which is worse than
//     showing it awkwardly.
func mermaidLabel(s string) string {
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, `"`, "\u201d")
	// Newlines end a statement in mermaid. Titles are single-line by
	// construction, so this is a backstop rather than a case.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// mermaidIDs assigns each node an identifier mermaid can parse.
//
// A project or action identifier already is one, and is used verbatim so the
// generated source reads like the queue it came from. A pull request key is
// not: `owner/repo#812` holds a slash and a hash, both of which end a node
// identifier. Sanitised rather than numbered for the same reason — `pr#812`
// tells a reader where a box came from and `n7` does not.
//
// Collisions are possible once two keys sanitise the same way, so a taken name
// gains a suffix. Deterministic, because the diagram is rebuilt on every
// request and a node that changes identifier between two renders of the same
// data is a diff nobody can read.
func mermaidIDs(nodes []graphNode) map[string]string {
	ids := make(map[string]string, len(nodes))
	taken := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		name := sanitiseID(n.ID)
		for i := 2; taken[name]; i++ {
			name = fmt.Sprintf("%s_%d", sanitiseID(n.ID), i)
		}
		taken[name] = true
		ids[n.ID] = name
	}
	return ids
}

func sanitiseID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	// An identifier starting with a digit is not one. Nothing in this schema
	// produces one, since every key begins with a prefix or an owner.
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "n" + name
	}
	return name
}
