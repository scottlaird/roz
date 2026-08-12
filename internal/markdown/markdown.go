// Package markdown renders the prose fields — the ones written as sentences
// rather than as values — and links the identifiers inside them.
//
// The database stores source text; this is the edge that renders it. Nothing
// here writes anything back, and `show -o json` and the event log keep
// returning exactly what the author typed.
package markdown

import (
	"bytes"
	"fmt"
	"html/template"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Identifiers worth turning into links wherever they appear in prose, because
// most of a title's references are written into the sentence rather than
// attached as a link: "close CDSS-1557 once resizing merges".
//
// A bare "#4156" is deliberately left alone. Which repository it means is a
// guess, and a link that goes confidently to the wrong pull request is worse
// than plain text.
var (
	jiraKeyPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b`)
	prPattern      = regexp.MustCompile(`\b([\w.-]+/[\w.-]+)#(\d+)\b`)
)

// Kinds of thing a link can point at.
const (
	KindPR   = "pr"
	KindJira = "jira"
)

// Target is what a link destination resolves to: a kind and the identifier
// roz knows that thing by.
//
// Resolution keys off the destination rather than off how the link was
// written, which is what lets a hand-written link be annotated as readily as
// an auto-linked identifier. `[cp#12345](https://github.com/acme/api/pull/12345)`
// keeps whatever text its author wanted and still resolves, because the href
// says unambiguously what it points at.
type Target struct {
	Kind string
	Key  string
}

// TitleFunc describes what a target is, for a tooltip. An empty string means
// nothing is known about it, and nothing is what should then be shown: a link
// to something untracked is better bare than captioned with a guess.
type TitleFunc func(Target) string

// A Linker knows which identifiers point somewhere, where they point, and —
// given a destination — what is there.
//
// It is shared by the two paths deliberately. Titles are plain text and prose
// fields are Markdown, so they are escaped and parsed quite differently, but
// what counts as an identifier must not depend on which field it was written
// in.
type Linker struct {
	jiraBase string
	jira     map[string]bool
	titles   TitleFunc
}

// prURLPattern matches the destination this package emits for a pull request,
// and the one a person writing the link out in full would use. The trailing
// segment is deliberately loose: GitHub redirects /issues/N to /pull/N for the
// same number, and both forms appear in prose.
var prURLPattern = regexp.MustCompile(
	`^https?://(?:www\.)?github\.com/([\w.-]+)/([\w.-]+)/(?:pull|issues)/(\d+)(?:[/?#].*)?$`)

// Resolve says what a link destination points at.
//
// Only shapes roz can name are resolved: a pull request in a repository it
// could be tracking, and a Jira issue under the configured base. Anything else
// is a link to the wider world, which this has nothing to add to.
func (l *Linker) Resolve(dest string) (Target, bool) {
	if m := prURLPattern.FindStringSubmatch(dest); m != nil {
		return Target{Kind: KindPR, Key: m[1] + "/" + m[2] + "#" + m[3]}, true
	}
	if l.jiraBase != "" && strings.HasPrefix(dest, l.jiraBase+"/") {
		key := strings.TrimPrefix(dest, l.jiraBase+"/")
		if key != "" && !strings.ContainsAny(key, "/?#") {
			return Target{Kind: KindJira, Key: key}, true
		}
	}
	return Target{}, false
}

// titleFor is the tooltip for a destination, or empty when there is none.
func (l *Linker) titleFor(dest string) string {
	if l.titles == nil {
		return ""
	}
	target, ok := l.Resolve(dest)
	if !ok {
		return ""
	}
	return Tooltip(l.titles(target))
}

// tooltipLimit is where a title is cut. A tooltip is a glance, not a place to
// read eighty characters plus a repository name, and browsers will render
// whatever length they are given on one line.
const tooltipLimit = 90

// Tooltip trims a title to something a hover can hold, cutting on a word
// boundary so the result reads as a truncated sentence rather than a truncated
// word.
func Tooltip(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if len(title) <= tooltipLimit {
		return title
	}
	cut := title[:tooltipLimit]
	if i := strings.LastIndex(cut, " "); i > tooltipLimit/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.") + "…"
}

// NewLinker configures linking. An empty jiraBase or an empty prefix list
// disables Jira links entirely.
//
// Jira keys are only linked when their project is one of the configured
// prefixes. The shape of a key is not distinctive enough to match on: the
// obvious pattern also matches UTF-8, SHA-256, ISO-8601 and CVE-2024, each of
// which would become a confident link to nothing.
func NewLinker(jiraBase string, jiraPrefixes []string, titles TitleFunc) *Linker {
	l := &Linker{jiraBase: strings.TrimSuffix(jiraBase, "/"), titles: titles}
	if l.jiraBase == "" {
		return l
	}
	l.jira = make(map[string]bool, len(jiraPrefixes))
	for _, p := range jiraPrefixes {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			l.jira[p] = true
		}
	}
	return l
}

// ref is one identifier found in a string: where it sits, and where it points.
type ref struct {
	start, end int
	dest       string
}

// find returns the identifiers in s, in order and never overlapping.
//
// Overlap is not hypothetical: a repository whose owner is spelled like a Jira
// project, as in ACME-1/tools#4, matches both patterns. Pull requests win
// because theirs is the longer, more specific match, and linking the ACME-1
// inside one would produce a link to a nonexistent issue in the middle of a
// repository name.
func (l *Linker) find(s string) []ref {
	var refs []ref
	for _, m := range prPattern.FindAllStringSubmatchIndex(s, -1) {
		refs = append(refs, ref{
			start: m[0],
			end:   m[1],
			dest:  "https://github.com/" + s[m[2]:m[3]] + "/pull/" + s[m[4]:m[5]],
		})
	}
	if len(l.jira) > 0 {
		for _, m := range jiraKeyPattern.FindAllStringIndex(s, -1) {
			key := s[m[0]:m[1]]
			project, _, _ := strings.Cut(key, "-")
			if !l.jira[project] || overlaps(refs, m[0], m[1]) {
				continue
			}
			refs = append(refs, ref{start: m[0], end: m[1], dest: l.jiraBase + "/" + key})
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].start < refs[j].start })
	}
	return refs
}

func overlaps(refs []ref, start, end int) bool {
	for _, r := range refs {
		if start < r.end && r.start < end {
			return true
		}
	}
	return false
}

// Text renders a field that is not Markdown: it escapes the text and links the
// identifiers in it, and does nothing else.
//
// Titles go through here. A title is a name, not a paragraph, and an author
// who writes "the *old* pipeline" there means the asterisks.
//
// Escaping first and marking the result as HTML is the safe order: nothing
// reaches the page unescaped, and the only markup added is what this function
// wrote.
func (l *Linker) Text(s string) template.HTML {
	refs := l.find(s)
	if len(refs) == 0 {
		return template.HTML(template.HTMLEscapeString(s))
	}

	var b strings.Builder
	at := 0
	for _, r := range refs {
		b.WriteString(template.HTMLEscapeString(s[at:r.start]))
		b.WriteString(`<a href="`)
		b.WriteString(template.HTMLEscapeString(r.dest))
		if title := l.titleFor(r.dest); title != "" {
			b.WriteString(`" title="`)
			b.WriteString(template.HTMLEscapeString(title))
		}
		b.WriteString(`">`)
		b.WriteString(template.HTMLEscapeString(s[r.start:r.end]))
		b.WriteString(`</a>`)
		at = r.end
	}
	b.WriteString(template.HTMLEscapeString(s[at:]))
	return template.HTML(b.String())
}

// A Renderer turns a Markdown prose field into HTML.
//
// It is safe to use from several goroutines at once, which the server relies
// on: goldmark.Markdown is stateless once built.
type Renderer struct {
	links *Linker
	md    goldmark.Markdown
}

// NewRenderer builds a renderer that links identifiers as it goes.
//
// Raw HTML is not enabled, so even a value that reached the database before
// Validate existed cannot inject markup.
func NewRenderer(links *Linker) *Renderer {
	return &Renderer{
		links: links,
		md: goldmark.New(goldmark.WithParserOptions(
			parser.WithASTTransformers(util.Prioritized(&linkTransformer{links: links}, 100)),
		)),
	}
}

// Render renders src as one or more block elements: a single sentence becomes
// a single <p>.
//
// Empty in, empty out — the template asks whether a field rendered to anything
// before it draws a container around it.
func (r *Renderer) Render(src string) template.HTML {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	var out bytes.Buffer
	if err := r.md.Convert([]byte(src), &out); err != nil {
		// Convert fails only when the writer does, which a bytes.Buffer does
		// not. Falling back to the plain rendering keeps a page that is merely
		// less pretty rather than one that is missing the text.
		return r.links.Text(src)
	}
	return template.HTML(out.String())
}

// linkTransformer rewrites identifiers into links after parsing, rather than
// before it.
//
// This is the whole reason prose fields need a parser at all. A regex over the
// source rewrites an identifier inside a code span or inside an existing
// link's text, producing broken code and nested anchors; a regex over the
// rendered HTML rewrites the inside of an href. Only the tree knows which
// runs of characters are prose.
type linkTransformer struct {
	links *Linker
}

func (t *linkTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()

	// Collected first and rewritten after: replacing a node splices its
	// parent's child list, which is not a thing to do to a walk in progress.
	var targets []*ast.Text
	var written []*ast.Link
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindLink:
			// A link the author wrote. Its children are its display text and
			// hold no identifiers to rewrite, but the link itself may point at
			// something worth captioning.
			if node, ok := n.(*ast.Link); ok {
				written = append(written, node)
			}
			return ast.WalkSkipChildren, nil
		case ast.KindAutoLink, ast.KindCodeSpan,
			ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindHTMLBlock, ast.KindRawHTML:
			return ast.WalkSkipChildren, nil
		case ast.KindText:
			if node, ok := n.(*ast.Text); ok && !node.IsRaw() {
				targets = append(targets, node)
			}
		}
		return ast.WalkContinue, nil
	})

	for _, node := range targets {
		t.rewrite(node, source)
	}
	// Hand-written links, annotated the same way the generated ones are: what
	// a link points at is a property of its destination, not of how it came to
	// be there.
	for _, node := range written {
		t.annotate(node)
	}
}

// annotate gives a link the title of whatever it points at, leaving a title
// the author wrote alone.
func (t *linkTransformer) annotate(node *ast.Link) {
	if len(node.Title) > 0 {
		return
	}
	if title := t.links.titleFor(string(node.Destination)); title != "" {
		node.Title = []byte(title)
	}
}

// rewrite replaces one text node with the alternating text and link nodes its
// identifiers imply.
func (t *linkTransformer) rewrite(node *ast.Text, source []byte) {
	segment := node.Segment
	parent := node.Parent()
	// Padding is the synthetic leading space of a continuation line, which
	// means the node's text is not exactly source[Start:Stop] and the offsets
	// below would be off. It is rare, and skipping it loses a link rather than
	// corrupting a line.
	if parent == nil || segment.Padding != 0 {
		return
	}
	value := string(source[segment.Start:segment.Stop])
	refs := t.links.find(value)
	if len(refs) == 0 {
		return
	}

	var last ast.Node
	insert := func(n ast.Node) {
		parent.InsertBefore(parent, node, n)
		last = n
	}

	at := 0
	for _, r := range refs {
		if r.start > at {
			insert(ast.NewTextSegment(text.NewSegment(segment.Start+at, segment.Start+r.start)))
		}
		link := ast.NewLink()
		link.Destination = []byte(r.dest)
		t.annotate(link)
		link.AppendChild(link, ast.NewTextSegment(
			text.NewSegment(segment.Start+r.start, segment.Start+r.end)))
		insert(link)
		at = r.end
	}
	if at < len(value) {
		insert(ast.NewTextSegment(text.NewSegment(segment.Start+at, segment.Stop)))
	}

	// A line break is a flag on the last text node of the line, not a node of
	// its own, so it has to be carried across. When the line ends on the link
	// itself there is no text node left to carry it, and an empty one is added
	// rather than letting two lines run together.
	if node.SoftLineBreak() || node.HardLineBreak() {
		tail, ok := last.(*ast.Text)
		if !ok {
			tail = ast.NewTextSegment(text.NewSegment(segment.Stop, segment.Stop))
			insert(tail)
		}
		tail.SetSoftLineBreak(node.SoftLineBreak())
		tail.SetHardLineBreak(node.HardLineBreak())
	}

	parent.RemoveChild(parent, node)
}

// validator parses for Validate. It renders nothing, so it needs none of the
// renderer's configuration.
var validator = goldmark.New()

// Validate reports what a Markdown field may not contain.
//
// There is only one rule, and that is the honest shape of this: almost any
// string is valid Markdown, so tagging a column format:"markdown" buys nothing
// at the input boundary by itself. The one thing worth refusing is raw HTML,
// and refusing it here says more than stripping it at render time would,
// because the author finds out immediately rather than wondering later why
// half a sentence vanished from the page.
//
// The parser decides what counts, rather than a second guess at Markdown's
// rules: <https://example.com> is an autolink and passes, while <b> and
// <!-- --> do not.
func Validate(src string) error {
	if src == "" {
		return nil
	}

	source := []byte(src)
	var found []byte
	ast.Walk(validator.Parser().Parse(text.NewReader(source)),
		func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch node := n.(type) {
			case *ast.RawHTML:
				if node.Segments.Len() > 0 {
					opening := node.Segments.At(0)
					found = opening.Value(source)
				}
			case *ast.HTMLBlock:
				if node.Lines().Len() > 0 {
					opening := node.Lines().At(0)
					found = opening.Value(source)
				}
			default:
				return ast.WalkContinue, nil
			}
			return ast.WalkStop, nil
		})

	if found == nil {
		return nil
	}
	return fmt.Errorf("raw HTML is not allowed here: %q — write it as Markdown, "+
		"or wrap it in backticks to show it literally", firstLine(found))
}

// firstLine keeps the error readable when the offending value is a whole
// pasted document.
func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}
