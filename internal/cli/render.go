package cli

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

//go:embed templates/page.html.tmpl
var templates embed.FS

// page is the status page's template, parsed once at startup so a broken one
// is a build-adjacent failure rather than a surprise at request time.
//
// html/template escapes what goes into it, which is why the blocks below are
// plain text: they are tabwriter output, and the only thing standing between
// a title full of angle brackets and the page is this.
var page = template.Must(template.ParseFS(templates, "templates/page.html.tmpl"))

// pageContent is what the template renders. The three blocks are
// preformatted text, and go inside <pre>.
type pageContent struct {
	GeneratedAt string
	Calendar    string
	Queue       string
	Projects    string
	// Live adds the script that reloads when the server says something
	// moved. A page written to a file has no server to listen to.
	Live bool
}

// horizon is how far ahead the calendar block looks. Two weeks is what a
// weekly review can act on; beyond that the answer is "ask again later".
const horizon = 14 * 24 * time.Hour

func newRenderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Regenerate the status page from the database",
		Long: "The page is a view; the database is the truth. Nothing here is\n" +
			"authoritative and nothing reads it back, so regenerating it is always\n" +
			"safe.\n\n" +
			"This version is three preformatted blocks and no design worth the\n" +
			"name: the calendar for the next fortnight, the unblocked actions, and\n" +
			"the live projects. It exists to be looked at, not admired.",
		Args: cobra.NoArgs,
		RunE: runRender,
	}
	cmd.Flags().String("out", "-", "output path, or - for stdout")
	return cmd
}

func runRender(cmd *cobra.Command, _ []string) error {
	path, err := cmd.Flags().GetString("out")
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	page, err := renderPage(cmd.Context(), st, time.Now(), false)
	if err != nil {
		return err
	}

	if path == "-" {
		_, err := cmd.OutOrStdout().Write(page)
		return err
	}
	if err := os.WriteFile(path, page, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), path)
	return nil
}

// renderPage builds the whole page in memory.
//
// In memory rather than streamed so that a failure half way through leaves
// the previous page in place: a status page that is truncated looks like an
// empty queue, which is the one wrong answer that matters.
func renderPage(ctx context.Context, st *store.Store, now time.Time, live bool) ([]byte, error) {
	windows, err := st.ListCalendarWindows(ctx, store.WindowFilter{
		Upcoming: true,
		Through:  now.Add(horizon).UTC().Format(store.DateFormat),
	})
	if err != nil {
		return nil, err
	}
	// The page is what gets read at a glance, so it is the one place that
	// does not print in creation order.
	actions, err := st.ListActions(ctx, store.ActionFilter{
		Unblocked: true, Order: store.OrderPriority,
	})
	if err != nil {
		return nil, err
	}
	projects, err := st.ListProjects(ctx, store.ProjectFilter{
		Status: store.ProjectActive, Order: store.OrderPriority,
	})
	if err != nil {
		return nil, err
	}
	jiraByProject, err := st.JiraByProject(ctx)
	if err != nil {
		return nil, err
	}

	content := pageContent{GeneratedAt: now.UTC().Format(time.RFC3339), Live: live}
	blocks := []struct {
		into  *string
		write func(io.Writer) error
	}{
		{&content.Calendar, func(w io.Writer) error { return writeCalendarTable(w, windows) }},
		{&content.Queue, func(w io.Writer) error { return writeRenderedActions(w, actions) }},
		{&content.Projects, func(w io.Writer) error { return writeRenderedProjects(w, projects, jiraByProject) }},
	}
	for _, b := range blocks {
		text, err := block(b.write)
		if err != nil {
			return nil, err
		}
		*b.into = text
	}

	var rendered bytes.Buffer
	if err := page.Execute(&rendered, content); err != nil {
		return nil, fmt.Errorf("rendering the page: %w", err)
	}
	return rendered.Bytes(), nil
}

// block runs one of the table writers and returns what it produced, as plain
// text. The template escapes it.
func block(write func(io.Writer) error) (string, error) {
	var raw bytes.Buffer
	if err := write(&raw); err != nil {
		return "", err
	}
	return raw.String(), nil
}

// writeRenderedActions is the queue, trimmed to what is worth reading at a
// glance: what to do, what it is for, and why it matters.
func writeRenderedActions(out io.Writer, actions []*store.Action) error {
	if len(actions) == 0 {
		fmt.Fprintln(out, "nothing to do")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tVERB\tPROJECT\tTITLE\tWHY")
	for _, a := range actions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			a.ID, a.Verb, nullText(a.ProjectID), a.Title, orDash(a.Why))
	}
	return w.Flush()
}

func writeRenderedProjects(out io.Writer, projects []*store.Project, jira map[string][]*store.JiraIssue) error {
	if len(projects) == 0 {
		fmt.Fprintln(out, "no active projects")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPRI\tEFFORT\tJIRA\tTITLE")
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			p.ID, nullIntText(p.Priority), nullText(p.Effort),
			jiraSummary(jira[p.ID]), p.Title)
	}
	return w.Flush()
}

// jiraSummary is the keys and their statuses, since neither says much alone:
// a key with no status means nothing has looked at it yet.
//
// A project may track several issues — scaling up and scaling down being two
// tickets for one piece of work — so this joins them rather than picking one.
func jiraSummary(issues []*store.JiraIssue) string {
	if len(issues) == 0 {
		return "-"
	}
	parts := make([]string, len(issues))
	for i, issue := range issues {
		parts[i] = issue.ID
		if issue.Status.Valid && issue.Status.String != "" {
			parts[i] += " (" + issue.Status.String + ")"
		}
	}
	return strings.Join(parts, ", ")
}
