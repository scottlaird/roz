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
			ids[n.ID], shapeFor(n.Kind, mermaidLabel(n.Label)), className(n.Class))
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
	case edgeContains:
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
// Mermaid reads # as the start of an entity code and " as the end of the
// label, so both have to become entity codes themselves — and # first, or the
// escape introduced for the quote is itself mangled. Everything else a title
// can hold is safe once it is inside the quotes; the page's own escaping
// happens afterwards and separately, since this text reaches the browser as
// HTML text and is read back out of the DOM.
func mermaidLabel(s string) string {
	s = strings.ReplaceAll(s, "#", "#35;")
	s = strings.ReplaceAll(s, `"`, "#quot;")
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
