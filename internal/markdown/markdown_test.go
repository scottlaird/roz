package markdown

import (
	"strings"
	"testing"
)

const base = "https://example.atlassian.net/browse"

func testLinker() *Linker { return NewLinker(base, []string{"CDSS"}, nil) }

// TestText covers the plain path: most of a title's references are written
// into the sentence rather than attached as a link, so the page has to find
// them there.
func TestText(t *testing.T) {
	links := testLinker()

	tests := []struct {
		name, in, want string
	}{
		{"a jira key in prose", "Close CDSS-1557 once resizing merges",
			`<a href="https://example.atlassian.net/browse/CDSS-1557">CDSS-1557</a>`},
		{"a qualified pull request", "follows temporalio/saas-infra-plane#4156",
			`<a href="https://github.com/temporalio/saas-infra-plane/pull/4156">temporalio/saas-infra-plane#4156</a>`},
		{"a bare number is left alone", "follows #4156", "#4156"},
		{"markup is escaped", "a <script>alert(1)</script> title", "&lt;script&gt;"},
		{"markdown is not interpreted", "the *old* pipeline", "the *old* pipeline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(links.Text(tt.in))
			if !strings.Contains(got, tt.want) {
				t.Errorf("Text(%q) = %q, want it to contain %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestTextLinksNothingItCannotPlace: a key's shape is not distinctive, so
// anything whose project is not configured has to stay plain text.
func TestTextLinksNothingItCannotPlace(t *testing.T) {
	links := testLinker()

	for _, in := range []string{
		"UTF-8 encoding", "SHA-256 digest", "ISO-8601 timestamps", "CVE-2024-1234", "RE-42",
	} {
		if got := string(links.Text(in)); strings.Contains(got, "<a") {
			t.Errorf("Text(%q) = %q, want no link: the project is not configured", in, got)
		}
	}

	// Without a base URL there is nowhere for a key to point, and guessing at
	// a host would produce links that look right and go nowhere.
	for _, l := range []*Linker{NewLinker("", []string{"CDSS"}, nil), NewLinker(base, nil, nil)} {
		if got := string(l.Text("Close CDSS-1557")); strings.Contains(got, "<a") {
			t.Errorf("Text linked with an unconfigured linker: %q", got)
		}
	}
}

// TestOverlappingIdentifiersResolveToTheLongerOne: an owner spelled like a
// Jira project sits inside the pull request's own match, and linking it would
// put an anchor in the middle of a repository name.
func TestOverlappingIdentifiersResolveToTheLongerOne(t *testing.T) {
	links := NewLinker(base, []string{"ACME"}, nil)

	got := string(links.Text("see ACME-1/tools#4 for the rest"))
	want := `<a href="https://github.com/ACME-1/tools/pull/4">ACME-1/tools#4</a>`
	if got != "see "+want+" for the rest" {
		t.Errorf("Text = %q, want the whole reference linked as %q", got, want)
	}
}

// TestRenderMarkdown: the prose fields are sentences, and the page should show
// them as the author wrote them.
func TestRenderMarkdown(t *testing.T) {
	r := NewRenderer(testLinker())

	tests := []struct {
		name, in, want string
	}{
		{"emphasis", "the *old* pipeline", "the <em>old</em> pipeline"},
		{"a code span", "run `roz sync github`", "<code>roz sync github</code>"},
		{"a list", "- first\n- second", "<li>first</li>"},
		{"an authored link", "see [the sketch](https://example.com/s)",
			`<a href="https://example.com/s">the sketch</a>`},
		{"an identifier is still linked", "blocked on CDSS-1557",
			`<a href="https://example.atlassian.net/browse/CDSS-1557">CDSS-1557</a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(r.Render(tt.in))
			if !strings.Contains(got, tt.want) {
				t.Errorf("Render(%q) = %q, want it to contain %q", tt.in, got, tt.want)
			}
		})
	}

	if got := r.Render("   \n  "); got != "" {
		t.Errorf("Render of blank prose = %q, want empty", got)
	}
}

// TestLinkingSkipsWhatIsNotProse is the reason this went through a parser
// instead of staying a regex. A regex over the string rewrites identifiers
// wherever it finds them, including the two places that break.
func TestLinkingSkipsWhatIsNotProse(t *testing.T) {
	r := NewRenderer(testLinker())

	tests := []struct {
		name, in, unwanted string
	}{
		{"inside a code span", "the key `CDSS-1557` is a literal", "<a"},
		{"inside a fenced block", "```\nCDSS-1557\n```", "<a"},
		{"inside a link's own text", "[CDSS-1557](https://example.com/elsewhere)",
			base + "/CDSS-1557"},
		{"inside a link's destination", "[the ticket](https://example.com/CDSS-1557)",
			`>https://example.com/CDSS-1557</a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(r.Render(tt.in))
			if strings.Contains(got, tt.unwanted) {
				t.Errorf("Render(%q) = %q, want it not to contain %q", tt.in, got, tt.unwanted)
			}
		})
	}

	// A link whose text is an identifier keeps exactly one anchor: the
	// author's. Nesting one inside it is invalid HTML and renders as garbage.
	got := string(r.Render("[CDSS-1557](https://example.com/elsewhere)"))
	if n := strings.Count(got, "<a "); n != 1 {
		t.Errorf("Render produced %d anchors, want 1:\n%s", n, got)
	}
}

// TestLinkingKeepsTheLineTogether: the line break is a flag on the text node,
// so replacing that node has to carry it, including when the line ends on the
// identifier itself.
func TestLinkingKeepsTheLineTogether(t *testing.T) {
	r := NewRenderer(testLinker())

	got := string(r.Render("blocked on CDSS-1557\nand nothing else"))
	if !strings.Contains(got, "</a>\nand nothing else") {
		t.Errorf("Render lost the line break:\n%s", got)
	}
}

// TestRenderEscapes: the source is authored text, and the renderer is the last
// thing between it and the page.
func TestRenderEscapes(t *testing.T) {
	r := NewRenderer(testLinker())

	got := string(r.Render("a <script>alert(1)</script> summary"))
	if strings.Contains(got, "<script>") {
		t.Errorf("Render passed a script tag through:\n%s", got)
	}
}

// TestValidateRejectsRawHTML is the whole of what the format tag buys at the
// input boundary, and it is worth having because the author finds out.
func TestValidateRejectsRawHTML(t *testing.T) {
	rejected := []string{
		"a <b>bold</b> claim",
		"<div>\nblock\n</div>",
		"<!-- a comment -->",
		"trailing <br>",
	}
	for _, in := range rejected {
		if err := Validate(in); err == nil {
			t.Errorf("Validate(%q) = nil, want an error", in)
		}
	}

	accepted := []string{
		"",
		"a plain sentence",
		"the *old* pipeline, `a < b`, and 5 < 6",
		"a literal `<b>` in backticks",
		"an autolink <https://example.com>",
		"see [the sketch](https://example.com/s)",
	}
	for _, in := range accepted {
		if err := Validate(in); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", in, err)
		}
	}
}

// TestValidateSaysWhatItFound: an error that does not quote the offending text
// leaves the author guessing which sentence it meant.
func TestValidateSaysWhatItFound(t *testing.T) {
	err := Validate("a <b>bold</b> claim")
	if err == nil {
		t.Fatal("Validate accepted raw HTML")
	}
	if !strings.Contains(err.Error(), "<b>") {
		t.Errorf("error = %v, want it to quote the tag", err)
	}
}

// titles is a lookup for the tests: one tracked pull request and one observed
// Jira issue, and nothing else known.
func titles() TitleFunc {
	known := map[Target]string{
		{Kind: KindPR, Key: "acme/api#1234"}: "Retry the upstream call on 503",
		{Kind: KindJira, Key: "CDSS-1744"}:   "Allow scaling the nodepool up",
	}
	return func(t Target) string { return known[t] }
}

func withTitles() *Linker {
	return NewLinker("https://example.atlassian.net/browse", []string{"CDSS"}, titles())
}

// renderWithTitles is the Markdown path with the same lookup behind it.
func renderWithTitles() *Renderer { return NewRenderer(withTitles()) }

func TestResolve(t *testing.T) {
	l := withTitles()

	for _, tc := range []struct {
		dest string
		want Target
		ok   bool
	}{
		{"https://github.com/acme/api/pull/1234", Target{KindPR, "acme/api#1234"}, true},
		// GitHub redirects between the two for a given number, and both forms
		// turn up in prose.
		{"https://github.com/acme/api/issues/1234", Target{KindPR, "acme/api#1234"}, true},
		{"https://github.com/acme/api/pull/1234#discussion_r1", Target{KindPR, "acme/api#1234"}, true},
		{"http://github.com/acme/api/pull/1234", Target{KindPR, "acme/api#1234"}, true},
		{"https://example.atlassian.net/browse/CDSS-1744", Target{KindJira, "CDSS-1744"}, true},
		// Not ours to caption.
		{"https://example.com/anything", Target{}, false},
		{"https://github.com/acme/api", Target{}, false},
		{"https://github.com/acme/api/pull/notanumber", Target{}, false},
		{"https://example.atlassian.net/browse/CDSS-1744/extra", Target{}, false},
	} {
		t.Run(tc.dest, func(t *testing.T) {
			got, ok := l.Resolve(tc.dest)
			if ok != tc.ok || got != tc.want {
				t.Errorf("Resolve(%q) = %v, %v; want %v, %v", tc.dest, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestTooltipOnAnAutoLinkedIdentifier: the identifier the linker found itself
// gets the same treatment as one written out.
func TestTooltipOnAnAutoLinkedIdentifier(t *testing.T) {
	got := string(withTitles().Text("blocked on acme/api#1234"))

	if !strings.Contains(got, `title="Retry the upstream call on 503"`) {
		t.Errorf("Text() = %q, want the pull request's title", got)
	}
}

// TestTooltipOnAHandWrittenLink is the case the design turns on. Keying off
// the destination rather than the syntax means an author keeps whatever
// display text they wanted and the link is still annotated.
func TestTooltipOnAHandWrittenLink(t *testing.T) {
	got := string(renderWithTitles().Render("see [cp#1234](https://github.com/acme/api/pull/1234)"))

	if !strings.Contains(got, `title="Retry the upstream call on 503"`) {
		t.Errorf("Render() = %q, want the tooltip on a hand-written link", got)
	}
	if !strings.Contains(got, ">cp#1234<") {
		t.Errorf("Render() = %q, want the author's own display text kept", got)
	}
}

func TestTooltipOnAJiraKey(t *testing.T) {
	got := string(renderWithTitles().Render("tracked as CDSS-1744"))

	if !strings.Contains(got, `title="Allow scaling the nodepool up"`) {
		t.Errorf("Render() = %q, want the issue summary", got)
	}
}

// TestNoTooltipWhenNothingIsKnown: absent rather than guessed. A blank title
// claims roz looked and found nothing, which is a worse thing to say on a
// hover than saying nothing at all.
func TestNoTooltipWhenNothingIsKnown(t *testing.T) {
	for _, src := range []string{
		"untracked acme/other#99",
		"unobserved CDSS-9999",
		"elsewhere [a thing](https://example.com/x)",
	} {
		t.Run(src, func(t *testing.T) {
			if got := string(renderWithTitles().Render(src)); strings.Contains(got, "title=") {
				t.Errorf("Render(%q) = %q, want no tooltip", src, got)
			}
		})
	}
}

// TestATitleTheAuthorWroteIsKept: Markdown has its own title syntax, and an
// author who used it meant it.
func TestATitleTheAuthorWroteIsKept(t *testing.T) {
	got := string(renderWithTitles().Render(
		`see [it](https://github.com/acme/api/pull/1234 "mine")`))

	if !strings.Contains(got, `title="mine"`) {
		t.Errorf("Render() = %q, want the author's title", got)
	}
}

// TestNoTooltipInsideACodeSpan: the identifier is being quoted as text, and
// nothing about it should become a link, captioned or otherwise.
func TestNoTooltipInsideACodeSpan(t *testing.T) {
	got := string(renderWithTitles().Render("write `acme/api#1234` literally"))

	if strings.Contains(got, "title=") || strings.Contains(got, "<a ") {
		t.Errorf("Render() = %q, want the code span left alone", got)
	}
}

func TestTooltipTruncation(t *testing.T) {
	long := "Make the scheduler retry transient upstream failures instead of " +
		"failing the whole batch, which has been biting us since March"

	got := Tooltip(long)
	if len(got) > tooltipLimit+3 {
		t.Errorf("Tooltip() = %q (%d bytes), want it cut near %d", got, len(got), tooltipLimit)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("Tooltip() = %q, want it to show it was cut", got)
	}
	// Cut on a word boundary, so it reads as a truncated sentence.
	if strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Errorf("Tooltip() = %q, want no trailing space before the ellipsis", got)
	}
	if short := "already short"; Tooltip(short) != short {
		t.Errorf("Tooltip(%q) = %q, want it untouched", short, Tooltip(short))
	}
	// Newlines in a stored title would break out of the attribute's line.
	if got := Tooltip("two\nlines"); got != "two lines" {
		t.Errorf("Tooltip() = %q, want the whitespace collapsed", got)
	}
}
