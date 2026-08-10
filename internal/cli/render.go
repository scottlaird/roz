package cli

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

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

	page, err := renderPage(cmd.Context(), st, time.Now())
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
func renderPage(ctx context.Context, st *store.Store, now time.Time) ([]byte, error) {
	windows, err := st.ListCalendarWindows(ctx, store.WindowFilter{
		Upcoming: true,
		Through:  now.Add(horizon).UTC().Format(store.DateFormat),
	})
	if err != nil {
		return nil, err
	}
	actions, err := st.ListActions(ctx, store.ActionFilter{Unblocked: true})
	if err != nil {
		return nil, err
	}
	projects, err := st.ListProjects(ctx, store.ProjectFilter{Status: store.ProjectActive})
	if err != nil {
		return nil, err
	}

	calendar, err := block(func(w io.Writer) error { return writeCalendarTable(w, windows) })
	if err != nil {
		return nil, err
	}
	queue, err := block(func(w io.Writer) error { return writeRenderedActions(w, actions) })
	if err != nil {
		return nil, err
	}
	planned, err := block(func(w io.Writer) error { return writeRenderedProjects(w, projects) })
	if err != nil {
		return nil, err
	}

	var page bytes.Buffer
	fmt.Fprintf(&page, pageTemplate,
		now.UTC().Format(time.RFC3339),
		calendar, queue, planned)
	return page.Bytes(), nil
}

// block runs one of the table writers and escapes what it produced, since it
// is going inside <pre>.
func block(write func(io.Writer) error) (string, error) {
	var raw bytes.Buffer
	if err := write(&raw); err != nil {
		return "", err
	}
	return html.EscapeString(raw.String()), nil
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

func writeRenderedProjects(out io.Writer, projects []*store.Project) error {
	if len(projects) == 0 {
		fmt.Fprintln(out, "no active projects")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPRI\tEFFORT\tJIRA\tTITLE")
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			p.ID, nullIntText(p.Priority), nullText(p.Effort),
			jiraSummary(p), p.Title)
	}
	return w.Flush()
}

// jiraSummary is the key and its status together, since neither says much
// alone: a key with no status means nothing has looked, and a status with no
// key cannot happen.
func jiraSummary(p *store.Project) string {
	if !p.JiraKey.Valid || p.JiraKey.String == "" {
		return "-"
	}
	if !p.JiraStatus.Valid || p.JiraStatus.String == "" {
		return p.JiraKey.String
	}
	return p.JiraKey.String + " (" + p.JiraStatus.String + ")"
}

const pageTemplate = `<!doctype html>
<meta charset="utf-8">
<title>todo</title>
<h1>todo</h1>
<p>generated %s</p>

<h2>calendar — the next fortnight</h2>
<pre>%s</pre>

<h2>queue — unblocked actions</h2>
<pre>%s</pre>

<h2>projects — active</h2>
<pre>%s</pre>
`
