package markdown

import "testing"

// TestPlainKeepsTheWordsAndDropsTheMarkup. The rule, in the cases that decide
// it: a link is its label, a code span is its contents, an image is its alt
// text, and a heading is a line of words.
func TestPlainKeepsTheWordsAndDropsTheMarkup(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "a code span keeps its contents",
			src:  "Fix `SL44` title-rendering problem",
			want: "Fix SL44 title-rendering problem",
		},
		{
			name: "a link is its label",
			src:  "See [the docs](https://example.com/x).",
			want: "See the docs.",
		},
		{
			name: "an autolink is its URL, which is all it has",
			src:  "See <https://example.com/y>.",
			want: "See https://example.com/y.",
		},
		{
			name: "emphasis is the words inside it",
			src:  "This is **important** and *this* is not",
			want: "This is important and this is not",
		},
		{
			name: "a heading is a line",
			src:  "# Heading\n\nBody.",
			want: "Heading\n\nBody.",
		},
		{
			name: "an image is its alt text",
			src:  "![a diagram](x.png) beside words",
			want: "a diagram beside words",
		},
		{
			name: "a fence keeps the code and loses the fence",
			src:  "```\ncode block\n```",
			want: "code block",
		},
		{
			name: "a rule has no words in it",
			src:  "before\n\n---\n\nafter",
			want: "before\n\nafter",
		},
		{
			name: "nothing in, nothing out",
			src:  "   ",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Plain(tt.src); got != tt.want {
				t.Errorf("Plain(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

// TestPlainKeepsAListALista. The bullets stay because they are the shape of
// the content rather than its decoration: read as a run-on sentence, a list is
// a different sentence.
func TestPlainKeepsAListAList(t *testing.T) {
	got := Plain("Before.\n\n- one\n  - nested\n- two\n\nAfter.")
	want := "Before.\n\n- one\n  - nested\n- two\n\nAfter."
	if got != want {
		t.Errorf("Plain() = %q, want %q", got, want)
	}
}

// TestPlainUnwrapsAWikiLink: the brackets are markup asking for a link, and a
// text rendering that kept them would be showing the source.
func TestPlainUnwrapsAWikiLink(t *testing.T) {
	if got, want := Plain("See [[SL8]] for the rest."), "See SL8 for the rest."; got != want {
		t.Errorf("Plain() = %q, want %q", got, want)
	}
}

// TestLineIsPlainOnOneLine, for a title bar or a log line, where a newline
// breaks the format rather than the sentence.
func TestLineIsPlainOnOneLine(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{"one paragraph\n\nand another", "one paragraph and another"},
		{"- a\n- b", "- a - b"},
		{"soft\nbreak", "soft break"},
		{"  leading and trailing  ", "leading and trailing"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Line(tt.src); got != tt.want {
			t.Errorf("Line(%q) = %q, want %q", tt.src, got, tt.want)
		}
	}
}

// TestPlainIsNotAStripOfTheHTML is the reason this is a second writer over the
// same parse. A regex over rendered HTML has to decide what a '<' means
// without a parser, and gets this wrong in both directions.
func TestPlainIsNotAStripOfTheHTML(t *testing.T) {
	// Angle brackets that are text, inside a code span, plus a real link whose
	// href holds a '>' — the case a tag-stripping regex cuts in half.
	src := "Use `a > b` in [the guide](https://example.com/?x=a>b)."
	want := "Use a > b in the guide."
	if got := Plain(src); got != want {
		t.Errorf("Plain(%q) = %q, want %q", src, got, want)
	}
}

// TestWikiBracketsGoWithNothingToResolveAgainst pins the fix this needed: the
// unwrapping used to be gated on the linker having something to resolve, so a
// database with nothing in it yet showed the brackets to a reader who did not
// write them — the opposite of what that code says it does.
func TestWikiBracketsGoWithNothingToResolveAgainst(t *testing.T) {
	empty := NewLinker(Config{})
	if got, want := string(empty.Text("See [[SL8]].")), "See SL8."; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}
