package cli

import (
	"strings"
	"testing"
)

// TestALabelCannotEndItsOwnLabel. Titles are free text and go straight into a
// quoted mermaid label, so the two characters that mean something there have
// to stop meaning it.
func TestALabelCannotEndItsOwnLabel(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"a quote would close the label", `say "no"`, `say #quot;no#quot;`},
		{"a hash starts an entity code", "fixes #812", "fixes #35;812"},
		// The order matters: escaping the quote introduces a hash, and
		// escaping hashes afterwards would mangle it into #35;quot;.
		{"both at once", `"#1"`, `#quot;#35;1#quot;`},
		{"a newline would end the statement", "one\ntwo", "one two"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mermaidLabel(tt.in); got != tt.want {
				t.Errorf("mermaidLabel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
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
