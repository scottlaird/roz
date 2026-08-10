package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"time"

	"github.com/scottlaird/todo/internal/store"
)

// The view model the status page renders.
//
// Structured rather than preformatted text, because the page's whole job is
// to be clicked: an identifier that is not a link costs a copy, a search and
// a guess about which repository it was in. The template escapes every field.
type pageContent struct {
	GeneratedAt string
	Stamp       string
	Windows     []windowView
	Queue       []actionView
	Waiting     []actionView
	Projects    []projectView
	// Live adds the script that reloads when the server says something moved.
	// A page written to a file has no server to listen to.
	Live bool
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
	ID       string
	Title    template.HTML
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
func buildPage(ctx context.Context, st *store.Store, now time.Time, live bool, jiraBase string, jiraPrefixes []string) (*pageContent, error) {
	today := now.UTC().Format(store.DateFormat)

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
	projects, err := st.ListProjects(ctx, store.ProjectFilter{Status: store.ProjectActive, Order: store.OrderPriority})
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
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Live:        live,
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

	inQueue := map[string]bool{}
	for _, a := range queue {
		inQueue[a.ID] = true
		content.Queue = append(content.Queue,
			actionRow(a, rank, prsByAction, jiraByProject, jiraBase, jiraPrefixes, today))
	}
	for _, a := range open {
		if inQueue[a.ID] || a.State != store.ActionReady ||
			a.HiddenBehind.Valid || rank[a.Verb] != store.RankWait {
			continue
		}
		content.Waiting = append(content.Waiting,
			actionRow(a, rank, prsByAction, jiraByProject, jiraBase, jiraPrefixes, today))
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
			Title:    linkify(p.Title, jiraBase, jiraPrefixes),
			Status:   p.Status,
			Priority: nullIntText(p.Priority),
			Effort:   nullText(p.Effort),
			Snooze:   nullText(p.SnoozeUntil),
			Actions:  openPerProject[p.ID],
			Jira:     jiraViews(jiraByProject[p.ID], jiraBase),
			Expired:  expired(p.SnoozeUntil, today),
		})
	}

	content.Stamp = fmt.Sprintf("%d in the queue · %d waiting · %d active projects",
		len(content.Queue), len(content.Waiting), len(content.Projects))
	return content, nil
}

func actionRow(a *store.Action, rank map[string]string, prs map[string][]store.ActionPR,
	jira map[string][]*store.JiraIssue, jiraBase string, jiraPrefixes []string, today string) actionView {

	view := actionView{
		ID:        a.ID,
		Title:     linkify(a.Title, jiraBase, jiraPrefixes),
		Why:       linkify(a.Why, jiraBase, jiraPrefixes),
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
		view.Jira = jiraViews(jira[a.ProjectID.String], jiraBase)
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

// Identifiers worth turning into links wherever they appear in prose, because
// most of a title's references are written into the sentence rather than
// attached as a link: "close CDSS-1557 once resizing merges".
var (
	jiraKeyPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b`)
	prPattern      = regexp.MustCompile(`\b([\w.-]+/[\w.-]+)#(\d+)\b`)
)

// linkify escapes the text and then turns identifiers in it into links.
//
// Escaping first and marking the result as HTML is the safe order: nothing
// reaches the page unescaped, and the only markup added is what this function
// wrote. Doing it the other way round -- linking, then trusting the template
// -- would double-escape the anchors.
//
// A bare "#4156" is deliberately left alone. Which repository it means is a
// guess, and a link that goes confidently to the wrong pull request is worse
// than plain text.
//
// Jira keys are only linked when their project is one of the configured
// prefixes. The shape of a key is not distinctive enough to match on: the
// obvious pattern also matches UTF-8, SHA-256, ISO-8601 and CVE-2024, each of
// which would become a confident link to nothing.
func linkify(text, jiraBase string, prefixes []string) template.HTML {
	escaped := template.HTMLEscapeString(text)

	escaped = prPattern.ReplaceAllString(escaped,
		`<a href="https://github.com/$1/pull/$2">$1#$2</a>`)

	if jiraBase != "" && len(prefixes) > 0 {
		allowed := make(map[string]bool, len(prefixes))
		for _, p := range prefixes {
			allowed[strings.ToUpper(strings.TrimSpace(p))] = true
		}
		base := strings.TrimSuffix(jiraBase, "/")
		escaped = jiraKeyPattern.ReplaceAllStringFunc(escaped, func(key string) string {
			project, _, _ := strings.Cut(key, "-")
			if !allowed[project] {
				return key
			}
			return `<a href="` + base + `/` + key + `">` + key + `</a>`
		})
	}
	return template.HTML(escaped)
}
