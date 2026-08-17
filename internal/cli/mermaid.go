package cli

import (
	"fmt"
	"strings"
)

// mermaid renders the graph as mermaid flowchart source.
//
// Text, produced on the server and drawn by the client. The layout is the part
// worth having and the part nothing here wants to write: a dependency diagram
// needs a layered layout with edge routing, and that is a solved problem
// belonging to whatever draws it.
//
// Left to right, so the arrows read as time: a blocker is to the left of what
// it blocks. Top-down puts long titles in a narrow column and turns a chain of
// four into a page of scrolling.
func mermaid(g *dependencyGraph) string {
	if g.Empty() {
		return ""
	}

	ids := mermaidIDs(g.Nodes)
	var b strings.Builder
	b.WriteString("graph LR\n")

	// No classDef here. Mermaid's own grammar cannot parse a CSS var() inside
	// one — the bracket ends the token — so a palette written into the diagram
	// source would have to be literal colours, and literal colours are a
	// stylesheet that does not follow the page into dark mode. The class name
	// reaches the SVG either way, so roz.css styles it like anything else.
	for _, n := range g.Nodes {
		fmt.Fprintf(&b, "  %s%s:::%s\n",
			ids[n.ID], shapeFor(n.Kind, mermaidLabel(n.Label)), mermaidClass(n.Class))
	}
	for _, e := range g.Edges {
		from, to := ids[e.From], ids[e.To]
		if from == "" || to == "" {
			continue // an edge to something pruning dropped; the node is the record
		}
		fmt.Fprintf(&b, "  %s %s %s\n", from, arrowFor(e.Kind), to)
	}
	// Links last, so the diagram reads as a diagram in source form and the
	// navigation is an appendix to it.
	for _, n := range g.Nodes {
		if n.Href != "" {
			fmt.Fprintf(&b, "  click %s href \"%s\"\n", ids[n.ID], n.Href)
		}
	}
	return b.String()
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
