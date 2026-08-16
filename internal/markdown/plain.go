package markdown

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// plainRenderer parses the way the HTML path does and writes text instead.
//
// The same transformer, so `[[SL8]]` becomes SL8 here as well as there: the
// wiki brackets are markup, and a text rendering that left them in would be
// the source rather than the rendering. It carries no Config, because a text
// rendering has no destinations to resolve — a link contributes its label
// whether or not anything would have been linked.
var plainRenderer = goldmark.New(goldmark.WithParserOptions(
	parser.WithASTTransformers(util.Prioritized(&linkTransformer{links: NewLinker(Config{})}, 100)),
))

// Plain renders Markdown as text, for the places that cannot draw markup: a
// browser tab, an alt attribute, a line of a log, a message somewhere else.
//
// It is a second writer over the same parse rather than a strip of the
// rendered HTML. Stripping tags with a regex is wrong on an attribute
// containing '>' and re-derives what the parser already knew; this asks the
// tree, which is the thing that knows a '#' opened a heading and a '`' opened
// a code span.
//
// What survives is what the prose said and not how it was marked up: a link
// becomes its label, a code span its contents, emphasis the words inside it.
// List items keep a "- " because a list read as a run-on sentence is a
// different sentence — that is the shape of the content, not its decoration.
func Plain(src string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	source := []byte(src)
	doc := plainRenderer.Parser().Parse(text.NewReader(source))

	var w plainWriter
	w.source = source
	w.walk(doc)
	return strings.Trim(w.b.String(), "\n")
}

// Line is Plain on one line, for somewhere a newline would break the format:
// a title bar, or the tail's one-line-per-event.
//
// Every run of whitespace becomes one space, so a paragraph break reads as a
// sentence break rather than disappearing into the word before it.
func Line(src string) string {
	return strings.Join(strings.Fields(Plain(src)), " ")
}

// plainWriter accumulates the text of a tree.
//
// Blocks are separated by a blank line, and the separator is written before
// the next block rather than after the last one, so the output does not end in
// whitespace nobody asked for.
type plainWriter struct {
	source []byte
	b      strings.Builder
	// depth is how many lists deep the walk is, for indenting nested items.
	depth int
}

func (w *plainWriter) walk(n ast.Node) {
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		w.node(child)
	}
}

func (w *plainWriter) node(n ast.Node) {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock, *ast.Heading:
		w.block()
		w.walk(n)
	case *ast.Blockquote:
		w.block()
		w.walk(n)
	case *ast.List:
		// The list is the block; its items are lines inside it. A nested one
		// is already inside that block and only needs its own line.
		if w.depth == 0 {
			w.block()
		} else {
			w.line()
		}
		w.depth++
		w.walk(n)
		w.depth--
	case *ast.ListItem:
		// One line each rather than a blank line between: a list is one block
		// whose items happen to be lines, and spacing them out would read as
		// several blocks.
		w.line()
		w.b.WriteString(strings.Repeat("  ", w.depth-1) + "- ")
		// The item's own paragraph must not open a block of its own, or the
		// bullet ends up on a line by itself.
		w.inlineBlocks(n)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		w.block()
		w.lines(n)
	case *ast.ThematicBreak:
		// A rule is decoration with no text in it at all.
	case *ast.Text:
		w.b.Write(n.Segment.Value(w.source))
		switch {
		case n.HardLineBreak():
			w.b.WriteString("\n")
		case n.SoftLineBreak():
			w.b.WriteString("\n")
		}
	case *ast.String:
		w.b.Write(n.Value)
	case *ast.CodeSpan:
		// The contents, without the backticks that said it was code.
		w.walk(n)
	case *ast.AutoLink:
		// It has no children: the URL is both the label and the destination.
		w.b.Write(n.URL(w.source))
	case *ast.Image:
		// The alt text, which is the only part of an image that is words.
		w.walk(n)
	case *ast.RawHTML, *ast.HTMLBlock:
		// Refused at the input boundary by Validate, and not text if it got
		// in some other way.
	default:
		w.walk(n)
	}
}

// inlineBlocks walks a list item's children without letting each open a block,
// which is what keeps an item on the line its bullet is on.
func (w *plainWriter) inlineBlocks(item ast.Node) {
	for child := item.FirstChild(); child != nil; child = child.NextSibling() {
		switch child.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			w.walk(child)
		default:
			w.node(child)
		}
	}
}

// lines writes a code block's own lines, which are segments rather than Text
// nodes.
func (w *plainWriter) lines(n ast.Node) {
	segments := n.Lines()
	for i := 0; i < segments.Len(); i++ {
		segment := segments.At(i)
		w.b.Write(segment.Value(w.source))
	}
}

// line starts a new line without opening a new block.
func (w *plainWriter) line() {
	if w.b.Len() == 0 || strings.HasSuffix(w.b.String(), "\n") {
		return
	}
	w.b.WriteString("\n")
}

// block opens a new block, separated from the last by a blank line.
func (w *plainWriter) block() {
	if w.b.Len() == 0 {
		return
	}
	current := w.b.String()
	switch {
	case strings.HasSuffix(current, "\n\n"):
	case strings.HasSuffix(current, "\n"):
		w.b.WriteString("\n")
	default:
		w.b.WriteString("\n\n")
	}
}
