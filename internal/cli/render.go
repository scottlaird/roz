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

	"github.com/scottlaird/roz/internal/store"
)

//go:embed templates/*.html.tmpl
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
// pages are the documents roz serves, each one the shared shell plus its own
// body.
//
// A set per page rather than one set with a switch: every body defines the
// same "body" block, which is what lets the shell be written once and know
// nothing about who fills it. Parsed at startup, so a broken template is a
// build-adjacent failure rather than a surprise at request time.
var pages = map[string]*template.Template{
	pageIndex:    parsePage("index"),
	pageProjects: parsePage("projects"),
	pageActions:  parsePage("actions"),
	pageProject:  parsePage("project"),
	pageAction:   parsePage("action"),
}

// The pages, named by the route that reaches them.
const (
	pageIndex    = ""
	pageProjects = "projects"
	pageActions  = "actions"
	pageProject  = "project"
	pageAction   = "action"
)

func parsePage(body string) *template.Template {
	return template.Must(template.ParseFS(templates,
		"templates/shell.html.tmpl", "templates/"+body+".html.tmpl"))
}

// page is the index, kept under its old name for the callers that only ever
// wanted that one.
var page = pages[pageIndex]

// stylesheet is the one static file the page needs.
const stylesheet = "roz.css"

// horizon is how far ahead the calendar block looks. Two weeks is what a
// weekly review can act on; beyond that the answer is "ask again later".
const horizon = 14 * 24 * time.Hour

// renderRoute builds one page: the index, a listing, or one entity.
//
// One renderer for all of them, since two would eventually disagree — and one
// document shell, so a page nobody thought about still has the heading, the
// stylesheet and the trail back.
func renderRoute(ctx context.Context, st *store.Store, now time.Time, live bool, at route) ([]byte, error) {
	tmpl, ok := pages[at.kind]
	if !ok {
		return nil, errNoSuchPage
	}
	settings, err := pageSettings(ctx, st)
	if err != nil {
		return nil, err
	}
	content, err := buildRoute(ctx, st, now, live, settings, at)
	if err != nil {
		return nil, err
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, content); err != nil {
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
