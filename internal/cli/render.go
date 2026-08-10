package cli

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"os"
	"strings"
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

// Set by --jira-base-url and --jira-prefix. Neither has a default: guessing
// at a host, or at what a key looks like, produces links that look right and
// go nowhere.
var (
	jiraBase     string
	jiraPrefixes []string
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
	content, err := buildPage(ctx, st, now, live, jiraBaseURL(), jiraProjectPrefixes())
	if err != nil {
		return nil, err
	}

	var rendered bytes.Buffer
	if err := page.Execute(&rendered, content); err != nil {
		return nil, fmt.Errorf("rendering the page: %w", err)
	}
	return rendered.Bytes(), nil
}

// jiraBaseURL is where a Jira key turns into a link. There is no sensible
// default -- every install has its own host -- so an unset one renders the
// key as plain text rather than guessing at somebody else's Jira.
func jiraBaseURL() string {
	if v := os.Getenv("TODO_JIRA_BASE_URL"); v != "" {
		return v
	}
	return jiraBase
}

// jiraProjectPrefixes are the project keys worth linking, e.g. CDSS. There is
// no default for the same reason: a key's shape is not distinctive, and the
// obvious pattern matches UTF-8 and SHA-256 as readily as CDSS-1744.
func jiraProjectPrefixes() []string {
	raw := jiraPrefixes
	if v := os.Getenv("TODO_JIRA_PREFIXES"); v != "" {
		raw = strings.Split(v, ",")
	}
	var out []string
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
