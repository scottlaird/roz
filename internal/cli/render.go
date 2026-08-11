package cli

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

//go:embed templates/page.html.tmpl
var templates embed.FS

//go:embed icon/roz-32.png
var iconPNG []byte

// favicon is the icon as a data URI, built once.
//
// Inline rather than served: the page is one file by design — written to disk
// by `roz render` as readily as served by `roz serve` — and a favicon fetched
// over a second request is one more thing that can fail, or simply not be
// there when the file is opened from disk. A 32px PNG costs under a kilobyte
// of base64, which is cheaper than the request would be.
//
// template.URL rather than string: html/template rejects a data: href as an
// unsafe scheme and substitutes ZgotmplZ. The exemption is safe here for the
// one reason it ever is — this value is a compile-time constant of our own,
// not anything that came from the database.
var favicon = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(iconPNG))

// page is the status page's template, parsed once at startup so a broken one
// is a build-adjacent failure rather than a surprise at request time.
//
// html/template escapes what goes into it, which is why the blocks below are
// plain text: they are tabwriter output, and the only thing standing between
// a title full of angle brackets and the page is this.
var page = template.Must(template.ParseFS(templates, "templates/page.html.tmpl"))

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
	cmd.Flags().String(flagOut, "-", "output path, or - for stdout")
	return cmd
}

func runRender(cmd *cobra.Command, _ []string) error {
	path, err := cmd.Flags().GetString(flagOut)
	if err != nil {
		return err
	}

	st, err := openStore(cmd)
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
	settings, err := pageSettings(ctx, st)
	if err != nil {
		return nil, err
	}
	content, err := buildPage(ctx, st, now, live, settings)
	if err != nil {
		return nil, err
	}

	var rendered bytes.Buffer
	if err := page.Execute(&rendered, content); err != nil {
		return nil, fmt.Errorf("rendering the page: %w", err)
	}
	return rendered.Bytes(), nil
}

// settings is what the page needs from `roz config`, resolved.
type settings struct {
	owner        string
	jiraBase     string
	jiraPrefixes []string
}

// pageSettings reads the settings for one page.
//
// The database is the source; the two environment variables override it for a
// single run, which is how you render somebody else's queue against your own
// Jira without writing that decision down. They are the only override left —
// these used to be flags, which meant passing them on every invocation and
// left anything reading the database directly with no way to know them.
func pageSettings(ctx context.Context, st *store.Store) (settings, error) {
	cfg, err := st.Config(ctx)
	if err != nil {
		return settings{}, err
	}
	prefixes, err := cfg.Prefixes()
	if err != nil {
		return settings{}, err
	}

	resolved := settings{
		owner:        cfg.Owner,
		jiraBase:     cfg.JiraBaseURL,
		jiraPrefixes: prefixes,
	}
	if v := os.Getenv("ROZ_JIRA_BASE_URL"); v != "" {
		resolved.jiraBase = v
	}
	if v := os.Getenv("ROZ_JIRA_PREFIXES"); v != "" {
		resolved.jiraPrefixes = splitPrefixes(v)
	}
	return resolved, nil
}

// splitPrefixes reads the comma-separated environment form. It does not
// validate: `config set` is where a typo gets refused, and refusing one here
// would fail a render over a setting it could simply not use.
func splitPrefixes(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.ToUpper(p))
		}
	}
	return out
}
