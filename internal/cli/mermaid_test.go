package cli

import (
	"strings"
	"testing"
)

// TestALabelSurvivesMermaid covers what a title has to go through, and every
// case here was found by rendering it in a browser rather than by reading the
// syntax. With htmlLabels off — which is what stops a title becoming markup —
// mermaid does not decode into SVG text, so its documented escapes are the
// wrong answer and two of them fail silently.
func TestALabelSurvivesMermaid(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		// The bug: #35; is mermaid's own entity code for a hash and renders as
		// the literal text "&#35;", which put "cel2sql&#35;168" on the page.
		{"a hash is left alone", "fixes #812", "fixes #812"},
		// A pair of backticks delimits a markdown string and mermaid drops
		// what is between them, so keeping them loses the word.
		{"backticks are removed, not their contents",
			"refuse `superseded_by` now", "refuse superseded_by now"},
		{"a lone backtick goes too", "a ` b", "a  b"},
		// A quote ends the label and no escape brings it back: #quot; renders
		// as "&quot;" and a backslash renders as a backslash.
		{"a quote cannot be represented, so it changes", `say "no"`, "say \u201dno\u201d"},
		{"a newline would end the statement", "one\ntwo", "one two"},
		// Left alone deliberately: the entity form produces identical output,
		// so there is nothing to gain by sending it.
		{"angle brackets are left as they are", "a < b > c", "a < b > c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mermaidLabel(tt.in); got != tt.want {
				t.Errorf("mermaidLabel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNoLabelCarriesAnEntityCode is the rule the cases above are instances of:
// nothing mermaid would have to decode may reach a label, because in this mode
// nothing decodes it and the code itself is what the reader sees.
func TestNoLabelCarriesAnEntityCode(t *testing.T) {
	for _, in := range []string{"#812", `"quoted"`, "a#b", `#"#`} {
		got := mermaidLabel(in)
		for _, bad := range []string{"#35;", "#quot;", "&#", "&quot;"} {
			if strings.Contains(got, bad) {
				t.Errorf("mermaidLabel(%q) = %q, which carries %q", in, got, bad)
			}
		}
	}
}

// TestAPullRequestKeyBecomesAnIdentifier: `owner/repo#812` holds a slash and a
// hash, both of which end a node identifier where it is used.
func TestAPullRequestKeyBecomesAnIdentifier(t *testing.T) {
	ids := mermaidIDs([]graphNode{
		{ID: "SL41"},
		{ID: "NA57"},
		{ID: "scottlaird/roz#812"},
	})
	if got := ids["SL41"]; got != "SL41" {
		t.Errorf("SL41 became %q; a queue identifier already is one", got)
	}
	got := ids["scottlaird/roz#812"]
	if strings.ContainsAny(got, "/#.") || got == "" {
		t.Errorf("scottlaird/roz#812 became %q, which mermaid cannot parse", got)
	}
	if !strings.Contains(got, "812") {
		t.Errorf("scottlaird/roz#812 became %q, which does not say where it came from", got)
	}
}

// TestTwoKeysCannotShareAnIdentifier. Sanitising is lossy, so two different
// keys can arrive at the same name — and two nodes with one identifier is one
// node with both sets of edges.
func TestTwoKeysCannotShareAnIdentifier(t *testing.T) {
	ids := mermaidIDs([]graphNode{
		{ID: "owner/repo#1"},
		{ID: "owner_repo_1"},
	})
	if ids["owner/repo#1"] == ids["owner_repo_1"] {
		t.Errorf("both became %q, so they would draw as one node", ids["owner/repo#1"])
	}
}

// TestTheSameGraphDrawsTheSameSource. The diagram is rebuilt on every request
// and reloaded whenever the log moves, so a node that changes identifier
// between two renders of the same data is a diff nobody can read.
func TestTheSameGraphDrawsTheSameSource(t *testing.T) {
	g := &dependencyGraph{
		Nodes: []graphNode{
			{ID: "SL1", Kind: nodeProject, Label: "one", Class: "active", Href: "/project/SL1"},
			{ID: "NA1", Kind: nodeAction, Label: "two", Class: "decide", Href: "/action/NA1"},
		},
		Edges: []graphEdge{{From: "NA1", To: "SL1", Kind: edgeAdvances}},
	}
	if first, second := mermaid(g), mermaid(g); first != second {
		t.Errorf("two renders of one graph differ:\n%s\n---\n%s", first, second)
	}
}

// TestAnUnknownStateDrawsAsPlain. A status nothing has styled would otherwise
// reach the page as a class name the stylesheet has never heard of, drawing as
// an unmarked box — which reads as "no state" rather than as "a state nobody
// thought about".
func TestAnUnknownStateDrawsAsPlain(t *testing.T) {
	if got := className("teleporting"); got != "plain" {
		t.Errorf("className(teleporting) = %q, want plain", got)
	}
	if got := className("blocked"); got != "blocked" {
		t.Errorf("className(blocked) = %q, want it kept", got)
	}
}

// TestNoClassCollidesWithMermaidsGrammar. `click` is how the navigation at the
// bottom of the source is written, so a node carrying `:::click` fails to parse
// and takes the rest of the diagram with it — the browser draws "Syntax error
// in text" and nothing else. Every other class parses, so this asserts the one
// rename rather than a general escape.
func TestNoClassCollidesWithMermaidsGrammar(t *testing.T) {
	g := &dependencyGraph{
		Nodes: []graphNode{
			{ID: "NA1", Kind: nodeAction, Label: "merge it", Class: "click", Href: "/action/NA1"},
			{ID: "NA2", Kind: nodeAction, Label: "decide it", Class: "decide"},
		},
	}
	src := mermaid(g)
	if strings.Contains(src, ":::click") {
		t.Errorf("a node still carries :::click, which mermaid cannot parse:\n%s", src)
	}
	if !strings.Contains(src, ":::oneclick") {
		t.Errorf("the one-click band lost its class entirely:\n%s", src)
	}
	if !strings.Contains(src, ":::decide") {
		t.Errorf("a class that does not collide was renamed anyway:\n%s", src)
	}
}

// TestNavigationIsNotInTheDiagramSource. A `click ... href` statement makes
// ELK draw nothing at all — not an error, an empty diagram — so the links go
// out beside the source and are bound to the drawn nodes instead. Worth a test
// because the failure is silent and the statement is the obvious way to do it.
func TestNavigationIsNotInTheDiagramSource(t *testing.T) {
	g := &dependencyGraph{
		Nodes: []graphNode{
			{ID: "SL1", Kind: nodeProject, Label: "one", Class: "active", Href: "/project/SL1"},
			{ID: "owner/repo#7", Kind: nodePR, Label: "two", Class: "wait",
				Href: "https://github.com/owner/repo/pull/7"},
			{ID: "NA1", Kind: nodeAction, Label: "three", Class: "decide"},
		},
		Edges: []graphEdge{{From: "SL1", To: "NA1", Kind: edgeAdvances}},
	}
	if src := mermaid(g); strings.Contains(src, "click ") {
		t.Errorf("the diagram source carries a click statement:\n%s", src)
	}

	links := mermaidLinks(g)
	if got := links["SL1"]; got != "/project/SL1" {
		t.Errorf("links[SL1] = %q, want the project page", got)
	}
	// Keyed by the diagram's name for it, not roz's: that is what the drawn
	// node's id is built from.
	ids := mermaidIDs(g.Nodes)
	if got := links[ids["owner/repo#7"]]; got != "https://github.com/owner/repo/pull/7" {
		t.Errorf("the pull request's link is not under %q: %v", ids["owner/repo#7"], links)
	}
	if _, ok := links["NA1"]; ok {
		t.Errorf("invented a link for a node that has none: %v", links)
	}
}

// TestEachKindOfEdgeDrawsDifferently, because they mean different things: a
// parent does not block a child, and drawing containment as a dependency
// arrow would say that it does.
func TestEachKindOfEdgeDrawsDifferently(t *testing.T) {
	seen := map[string]string{}
	for _, kind := range []string{edgeBlocks, edgeContains, edgeAdvances, edgeHides} {
		arrow := arrowFor(kind)
		if other, clash := seen[arrow]; clash {
			t.Errorf("%s and %s both draw as %q", kind, other, arrow)
		}
		seen[arrow] = kind
	}
}

// TestAnEdgeToAPrunedNodeIsDropped. Pruning works on nodes, so an edge can
// outlive one of its ends; emitting it would make mermaid invent the missing
// node as an empty box.
func TestAnEdgeToAPrunedNodeIsDropped(t *testing.T) {
	g := &dependencyGraph{
		Nodes: []graphNode{{ID: "SL1", Kind: nodeProject, Label: "one", Class: "active"}},
		Edges: []graphEdge{{From: "SL1", To: "SL999", Kind: edgeBlocks}},
	}
	if out := mermaid(g); strings.Contains(out, "SL999") {
		t.Errorf("drew an edge to a node that is not in the graph:\n%s", out)
	}
}

// TestTheTitleIsTrimmedRatherThanWrapped. A diagram is read by scanning it,
// and a box holding a paragraph is a box nobody can place.
func TestTheTitleIsTrimmedRatherThanWrapped(t *testing.T) {
	long := strings.Repeat("word ", 40)
	label := nodeLabel("SL1", long, "2")
	if len(label) > nodeLabelWidth+16 {
		t.Errorf("label is %d characters: %q", len(label), label)
	}
	for _, want := range []string{"SL1", "…", "(2)"} {
		if !strings.Contains(label, want) {
			t.Errorf("label %q is missing %q", label, want)
		}
	}
}
