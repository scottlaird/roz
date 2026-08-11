package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/scottlaird/roz/internal/markdown"
	"github.com/scottlaird/roz/internal/store"
)

// prose is everything the page needs in order to turn stored text into
// clickable HTML: what counts as an identifier, how to render a Markdown
// field, and where a Jira key points.
//
// It is built once per page and passed down, rather than threading a base URL
// and a prefix list through every row function. Building the renderer once
// also matters: goldmark assembles a parser and a renderer at construction,
// and doing that per action would be work for nothing.
type prose struct {
	links    *markdown.Linker
	markdown *markdown.Renderer
	jiraBase string
}

func newProse(jiraBase string, jiraPrefixes []string) *prose {
	links := markdown.NewLinker(jiraBase, jiraPrefixes)
	return &prose{
		links:    links,
		markdown: markdown.NewRenderer(links),
		jiraBase: strings.TrimSuffix(jiraBase, "/"),
	}
}

// The view model the status page renders.
//
// Structured rather than preformatted text, because the page's whole job is
// to be clicked: an identifier that is not a link costs a copy, a search and
// a guess about which repository it was in. The template escapes every field.
type pageContent struct {
	// Owner is whose queue this is, from `roz config`. Empty is normal and
	// the heading simply reads "roz".
	Owner       string
	GeneratedAt string
	Stamp       string
	Windows     []windowView
	Queue       []actionView
	Waiting     []actionView
	Projects    []projectView
	// Live adds the script that reloads when the server says something moved.
	// A page written to a file has no server to listen to.
	Live bool
	// Favicon is the icon inline, as a data URI. See favicon in render.go for
	// why it is not a file the page asks for, and why it is a template.URL.
	Favicon template.URL
}

type windowView struct {
	Label    string
	Kind     string
	Range    string
	Capacity string
	Current  bool
}

type actionView struct {
	ID        string
	Title     template.HTML
	Why       template.HTML
	Verb      string
	RankClass string
	Project   string
	Age       string
	PRs       []prView
	Jira      []jiraView
	Expired   bool
}

type prView struct {
	ID     string
	URL    string
	Status string
	Bad    bool
}

type jiraView struct {
	Key    string
	URL    string
	Status string
}

type projectView struct {
	ID string
	// Title is a name and stays literal; Summary is prose and renders as
	// Markdown. Both link the same identifiers.
	Title    template.HTML
	Summary  template.HTML
	Status   string
	Priority string
	Effort   string
	Snooze   string
	Actions  int
	Jira     []jiraView
	Expired  bool
}

// buildPage assembles everything the template needs.
//
// Every lookup here is batched. A per-row query would be invisible at this
// size and wrong at any other, and the shape is the thing that gets copied.
func buildPage(ctx context.Context, st *store.Store, now time.Time, live bool, cfg settings) (*pageContent, error) {
	today := now.UTC().Format(store.DateFormat)
	text := newProse(cfg.jiraBase, cfg.jiraPrefixes)

	windows, err := st.ListCalendarWindows(ctx, store.WindowFilter{
		Upcoming: true,
		Through:  now.Add(horizon).UTC().Format(store.DateFormat),
	})
	if err != nil {
		return nil, err
	}
	// Unblocked is the queue. Waiting is what the queue leaves out *because it
	// is waiting on someone* -- ready, unhidden, and a wait verb. Blocked and
	// hidden actions stay out of both: their blocker is already in the queue,
	// and listing them is the noise the fold rule exists to stop.
	queue, err := st.ListActions(ctx, store.ActionFilter{Unblocked: true, Order: store.OrderPriority})
	if err != nil {
		return nil, err
	}
	open, err := st.ListActions(ctx, store.ActionFilter{Open: true, Order: store.OrderPriority})
	if err != nil {
		return nil, err
	}
	// The same definition the CLI's --waiting uses, rather than a second one
	// here: a queue and the things it deliberately omits should not be able
	// to disagree about what is in play.
	waiting, err := st.ListActions(ctx, store.ActionFilter{Waiting: true, Order: store.OrderPriority})
	if err != nil {
		return nil, err
	}
	// Open rather than active: a blocked project is live work, and hiding it
	// is how one sat unseen for a session. It shows with its status, which is
	// the whole information.
	projects, err := st.ListProjects(ctx, store.ProjectFilter{Open: true, Order: store.OrderPriority})
	if err != nil {
		return nil, err
	}
	jiraByProject, err := st.JiraByProject(ctx)
	if err != nil {
		return nil, err
	}
	prsByAction, err := st.PRsByAction(ctx)
	if err != nil {
		return nil, err
	}
	verbs, err := st.ListVerbs(ctx, false)
	if err != nil {
		return nil, err
	}
	rank := map[string]string{}
	for _, v := range verbs {
		rank[v.Verb] = v.RankClass
	}

	content := &pageContent{
		Owner:       cfg.owner,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Live:        live,
		Favicon:     favicon,
	}

	for _, w := range windows {
		content.Windows = append(content.Windows, windowView{
			Label:    w.Label,
			Kind:     w.Kind,
			Range:    windowRange(w),
			Capacity: w.Capacity,
			Current:  w.Covers(today),
		})
	}

	for _, a := range queue {
		content.Queue = append(content.Queue,
			actionRow(a, rank, prsByAction, jiraByProject, text, today))
	}
	for _, a := range waiting {
		content.Waiting = append(content.Waiting,
			actionRow(a, rank, prsByAction, jiraByProject, text, today))
	}

	openPerProject := map[string]int{}
	for _, a := range open {
		if a.ProjectID.Valid {
			openPerProject[a.ProjectID.String]++
		}
	}
	for _, p := range projects {
		content.Projects = append(content.Projects, projectView{
			ID:       p.ID,
			Title:    text.links.Text(p.Title),
			Summary:  text.markdown.Render(p.Summary),
			Status:   p.Status,
			Priority: nullIntText(p.Priority),
			Effort:   nullText(p.Effort),
			Snooze:   nullText(p.SnoozeUntil),
			Actions:  openPerProject[p.ID],
			Jira:     jiraViews(jiraByProject[p.ID], text.jiraBase),
			Expired:  expired(p.SnoozeUntil, today),
		})
	}

	content.Stamp = fmt.Sprintf("%d in the queue · %d waiting · %d open projects",
		len(content.Queue), len(content.Waiting), len(content.Projects))
	return content, nil
}

func actionRow(a *store.Action, rank map[string]string, prs map[string][]store.ActionPR,
	jira map[string][]*store.JiraIssue, text *prose, today string) actionView {

	view := actionView{
		ID: a.ID,
		// A title is a name and renders as plain text; why is prose and
		// renders as Markdown. Both link the same identifiers.
		Title:     text.links.Text(a.Title),
		Why:       text.markdown.Render(a.Why),
		Verb:      a.Verb,
		RankClass: rank[a.Verb],
		Project:   nullText(a.ProjectID),
		Age:       age(a, today),
		Expired:   expired(a.SnoozeUntil, today),
	}
	for _, p := range prs[a.ID] {
		view.PRs = append(view.PRs, prRow(p))
	}
	if a.ProjectID.Valid {
		view.Jira = jiraViews(jira[a.ProjectID.String], text.jiraBase)
	}
	return view
}

// prRow reduces a pull request to the one line worth showing, and says
// whether it is the sort of state somebody has to act on.
//
// Merged and approved-and-clean are not problems; DIRTY and BEHIND are, and
// so is a failing check. Everything else is ordinary waiting.
func prRow(p store.ActionPR) prView {
	view := prView{ID: p.ID, URL: nullText(p.URL)}
	if view.URL == "-" {
		view.URL = ""
	}

	var parts []string
	state := nullText(p.State)
	if state != "-" {
		parts = append(parts, strings.ToLower(state))
	}
	if p.IsDraft.Valid && p.IsDraft.Bool {
		parts = append(parts, "draft")
	}
	if d := nullText(p.ReviewDecision); d != "-" {
		parts = append(parts, strings.ToLower(strings.ReplaceAll(d, "_", " ")))
	}
	if m := nullText(p.MergeStateStatus); m == "DIRTY" || m == "BEHIND" {
		parts = append(parts, strings.ToLower(m))
		view.Bad = true
	}
	if c := nullText(p.ChecksState); c == "FAILURE" || c == "ERROR" {
		parts = append(parts, "checks failing")
		view.Bad = true
	}
	if p.InMergeQueue.Valid && p.InMergeQueue.Bool {
		parts = append(parts, "in merge queue")
	}
	if p.Frozen {
		parts = append(parts, "frozen")
	}
	if teams := jsonList(p.ReviewerTeams); len(teams) > 0 {
		parts = append(parts, "waiting on "+strings.Join(teams, ", "))
	}
	view.Status = strings.Join(parts, " · ")
	return view
}

func jiraViews(issues []*store.JiraIssue, base string) []jiraView {
	views := make([]jiraView, 0, len(issues))
	for _, issue := range issues {
		view := jiraView{Key: issue.ID, Status: nullText(issue.Status)}
		if view.Status == "-" {
			view.Status = ""
		}
		if base != "" {
			view.URL = strings.TrimSuffix(base, "/") + "/" + issue.ID
		}
		views = append(views, view)
	}
	return views
}

// age is the short chip on the right of a queue item: what state it is in and
// since when, which is the difference between being patient and nobody having
// looked.
func age(a *store.Action, today string) string {
	if a.SnoozeUntil.Valid {
		if a.SnoozeUntil.String < today {
			return "due " + a.SnoozeUntil.String
		}
		return "until " + a.SnoozeUntil.String
	}
	if a.WaitingSince.Valid && a.WaitingSince.String != "" {
		return "since " + shortDate(a.WaitingSince.String)
	}
	if teams := jsonList(a.WaitingOn); len(teams) > 0 {
		return "waiting on " + strings.Join(teams, ", ")
	}
	return ""
}

func windowRange(w *store.CalendarWindow) string {
	if w.StartsOn == w.EndsOn {
		return w.StartsOn
	}
	return w.StartsOn + " → " + w.EndsOn
}

func expired(until sql.NullString, today string) bool {
	return until.Valid && until.String != "" && until.String < today
}

func jsonList(raw string) []string {
	var out []string
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func shortDate(stamp string) string {
	if len(stamp) >= 10 {
		return stamp[:10]
	}
	return stamp
}
