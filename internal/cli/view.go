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

func newProse(jiraBase string, jiraPrefixes []string, repos map[string]string,
	refs map[string]markdown.Ref, titles markdown.TitleFunc) *prose {

	links := markdown.NewLinker(markdown.Config{
		JiraBase:     jiraBase,
		JiraPrefixes: jiraPrefixes,
		Repos:        repos,
		Refs:         refs,
		Titles:       titles,
	})
	return &prose{
		links:    links,
		markdown: markdown.NewRenderer(links),
		jiraBase: strings.TrimSuffix(jiraBase, "/"),
	}
}

// titlesFrom builds the tooltip lookup: what roz knows about each thing a link
// can point at.
//
// Read at render, so a renamed pull request shows its new title with nothing
// re-synced. That is the argument for looking them up here rather than baking
// them in when the link is written.
//
// Anything not tracked, and anything tracked but never observed, is absent
// rather than empty. A link with no tooltip says nothing; a link with a blank
// one says roz looked and found nothing, which is a different and less useful
// claim to make on a hover.
func titlesFrom(prs []*store.PR, issues []*store.TrackerIssue,
	actions []*store.Action, projects []*store.Project) markdown.TitleFunc {
	known := make(map[markdown.Target]string, len(prs)+len(issues))
	for _, pr := range prs {
		if pr.Title != "" {
			known[markdown.Target{Kind: markdown.KindPR, Key: pr.ID}] = pr.Title
		}
	}
	for _, issue := range issues {
		// Only Jira answers for a Jira URL. A GitHub issue tracked under the
		// same key would be a different thing at a different address.
		if issue.Tracker == store.TrackerJira && issue.Summary != "" {
			known[markdown.Target{Kind: markdown.KindJira, Key: issue.Key}] = issue.Summary
		}
	}
	for _, a := range actions {
		known[markdown.Target{Kind: markdown.KindAction, Key: a.ID}] = a.Title
	}
	for _, p := range projects {
		known[markdown.Target{Kind: markdown.KindProject, Key: p.ID}] = p.Title
	}
	return func(target markdown.Target) string { return known[target] }
}

// refsFor says where every action and project lives on the page.
//
// The anchor is the identifier verbatim. Prefixes are configurable, so a
// scheme like action-NA57 would need to know which prefix means which kind,
// and the identifier is already unique across both — the two prefixes cannot
// be the same. It is also what a person would guess, which matters: an anchor
// is a public surface, and once a link to one exists in somebody's notes,
// changing the scheme breaks it silently.
func refsFor(actions []*store.Action, projects []*store.Project) map[string]markdown.Ref {
	refs := make(map[string]markdown.Ref, len(actions)+len(projects))
	for _, a := range actions {
		refs[a.ID] = markdown.Ref{Kind: markdown.KindAction, Href: "#" + a.ID}
	}
	for _, p := range projects {
		refs[p.ID] = markdown.Ref{Kind: markdown.KindProject, Href: "#" + p.ID}
	}
	return refs
}

// remaining returns the entities not already shown, so each appears on the
// page exactly once.
//
// Exactly once is the constraint that matters: an id has to be unique in a
// document, so an action cannot be anchored both in the queue and in an index
// of everything. Splitting the set rather than repeating it means the anchor
// is wherever the entity is, and a link never has to know which section that
// turned out to be.
func remaining[T any](all []T, id func(T) string, shown ...[]string) []T {
	seen := map[string]bool{}
	for _, list := range shown {
		for _, s := range list {
			seen[s] = true
		}
	}
	var out []T
	for _, item := range all {
		if !seen[id(item)] {
			out = append(out, item)
		}
	}
	return out
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
	// Elsewhere is every action the two lists above leave out: closed,
	// blocked, hidden, or snoozed and not yet due. It exists so that a
	// reference to any action has somewhere to land.
	Elsewhere []actionView
	// Closed is the same for projects the table above omits.
	Closed []projectView
	// Notes is the authored prose, by slot. A slot with nothing in it is
	// absent rather than empty, so the template can ask without guarding.
	Notes map[string]noteView
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
	ID    string
	Title template.HTML
	Why   template.HTML
	Verb  string
	// State is only drawn in the index of everything, where an action may be
	// closed, blocked or deferred. The queue and the waits are all ready by
	// construction, so showing it there would say the same word every time.
	State     string
	RankClass string
	Project   template.HTML
	Age       string
	PRs       []prView
	Issues    []issueView
	Expired   bool
	// Late is how many days past its allowance this action is, empty when it
	// is not. An action already in the queue is told about by marking it here
	// rather than by raising a second item about itself, so without this the
	// allowance on a verb like `merge` would say nothing anybody could see.
	Late string
}

// noteView is one keyed prose block, rendered.
type noteView struct {
	Body template.HTML
	// Set is when it was last written. Worth showing: a note is authored and
	// nothing revisits it, so how old it is says how much to trust it.
	Set string
}

type prView struct {
	ID     string
	URL    string
	Status string
	Bad    bool
}

type issueView struct {
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
	Issues   []issueView
	Expired  bool
}

// buildPage assembles everything the template needs.
//
// Every lookup here is batched. A per-row query would be invisible at this
// size and wrong at any other, and the shape is the thing that gets copied.
func buildPage(ctx context.Context, st *store.Store, now time.Time, live bool, cfg settings) (*pageContent, error) {
	// Loaded before the prose renderer, which needs them to caption links.
	// Everything tracked, not only what this page happens to draw: prose can
	// reference a pull request no action on the page is about.
	allPRs, err := st.ListPRs(ctx, store.PRFilter{})
	if err != nil {
		return nil, err
	}
	allIssues, err := st.ListTrackerIssues(ctx)
	if err != nil {
		return nil, err
	}
	shortNames, err := st.ShortNames(ctx)
	if err != nil {
		return nil, err
	}
	pageNotes, err := st.PageNotes(ctx)
	if err != nil {
		return nil, err
	}
	// Everything, for two reasons: an identifier only links if it exists, and
	// every identifier needs a row on the page to link to.
	everyAction, err := st.ListActions(ctx, store.ActionFilter{})
	if err != nil {
		return nil, err
	}
	everyProject, err := st.ListProjects(ctx, store.ProjectFilter{})
	if err != nil {
		return nil, err
	}

	today := now.UTC().Format(store.DateFormat)
	// A calendar window is a span of days and asks about the date; a snooze is
	// compared against the instant the queries use. See expired.
	stamp := now.UTC().Format(store.TimeFormat)
	text := newProse(cfg.jiraBase, cfg.jiraPrefixes, shortNames,
		refsFor(everyAction, everyProject),
		titlesFrom(allPRs, allIssues, everyAction, everyProject))

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
	issuesByProject, err := st.IssuesByProject(ctx)
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

	late, err := st.LateActions(ctx)
	if err != nil {
		return content, err
	}
	for _, a := range queue {
		content.Queue = append(content.Queue,
			actionRow(a, rank, prsByAction, issuesByProject, text, stamp, late))
	}
	for _, a := range waiting {
		content.Waiting = append(content.Waiting,
			actionRow(a, rank, prsByAction, issuesByProject, text, stamp, late))
	}

	openPerProject := map[string]int{}
	for _, a := range open {
		if a.ProjectID.Valid {
			openPerProject[a.ProjectID.String]++
		}
	}
	// Every action the two lists above left out, so a reference to any of them
	// has a row to land on.
	for _, a := range remaining(everyAction, func(a *store.Action) string { return a.ID },
		ids(queue), ids(waiting)) {
		content.Elsewhere = append(content.Elsewhere,
			actionRow(a, rank, prsByAction, issuesByProject, text, stamp, late))
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
			Issues:   issueViews(issuesByProject[p.ID], text.jiraBase),
			Expired:  expired(p.SnoozeUntil, stamp),
		})
	}

	for _, p := range remaining(everyProject, func(p *store.Project) string { return p.ID },
		projectIDs(projects)) {
		content.Closed = append(content.Closed, projectView{
			ID:       p.ID,
			Title:    text.links.Text(p.Title),
			Status:   p.Status,
			Priority: nullIntText(p.Priority),
			Effort:   nullText(p.Effort),
		})
	}

	// Rendered after the linker exists, so a note naming SL7 links it the way
	// a project summary does.
	for slot, note := range pageNotes {
		if strings.TrimSpace(note.Body) == "" {
			continue // an emptied slot draws nothing, not an empty box
		}
		if content.Notes == nil {
			content.Notes = map[string]noteView{}
		}
		content.Notes[slot] = noteView{
			Body: text.markdown.Render(note.Body),
			Set:  shortDate(note.UpdatedAt),
		}
	}

	content.Stamp = fmt.Sprintf("%d in the queue · %d waiting · %d open projects",
		len(content.Queue), len(content.Waiting), len(content.Projects))
	return content, nil
}

func ids(actions []*store.Action) []string {
	out := make([]string, len(actions))
	for i, a := range actions {
		out[i] = a.ID
	}
	return out
}

func projectIDs(projects []*store.Project) []string {
	out := make([]string, len(projects))
	for i, p := range projects {
		out[i] = p.ID
	}
	return out
}

func actionRow(a *store.Action, rank map[string]string, prs map[string][]store.ActionPR,
	issues map[string][]*store.TrackerIssue, text *prose, now string,
	late map[string]int) actionView {

	view := actionView{
		ID: a.ID,
		// A title is a name and renders as plain text; why is prose and
		// renders as Markdown. Both link the same identifiers.
		Title:     text.links.Text(a.Title),
		Why:       text.markdown.Render(a.Why),
		Verb:      a.Verb,
		State:     a.State,
		RankClass: rank[a.Verb],
		Project:   projectLink(a.ProjectID, text),
		Age:       age(a, now),
		Expired:   expired(a.SnoozeUntil, now),
		Late:      lateLabel(late, a.ID),
	}
	for _, p := range prs[a.ID] {
		view.PRs = append(view.PRs, prRow(p))
	}
	if a.ProjectID.Valid {
		view.Issues = issueViews(issues[a.ProjectID.String], text.jiraBase)
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
	// Who GitHub was asked for, and who the files say is needed. They differ
	// where a repository does not enforce CODEOWNERS: GitHub requests nobody
	// while the file still describes who ought to look, which is exactly the
	// case where saying so is worth something.
	if teams := jsonList(p.ReviewerTeams); len(teams) > 0 {
		parts = append(parts, "waiting on "+strings.Join(teams, ", "))
	} else if owners := outstandingOwners(p.PR); len(owners) > 0 {
		parts = append(parts, "needs "+strings.Join(owners, ", "))
	}
	view.Status = strings.Join(parts, " · ")
	return view
}

// issueViews renders an issue for the page.
//
// The link is built from the tracker's own key, not from the composed id: the
// id carries a `jira:` prefix that means something here and nothing to Jira.
// Only Jira has a configured base, so an issue from any other tracker renders
// as text until something knows how to address it.
func issueViews(issues []*store.TrackerIssue, base string) []issueView {
	views := make([]issueView, 0, len(issues))
	for _, issue := range issues {
		view := issueView{Key: issue.Key, Status: nullText(issue.Status)}
		if view.Status == "-" {
			view.Status = ""
		}
		if base != "" && issue.Tracker == store.TrackerJira {
			view.URL = strings.TrimSuffix(base, "/") + "/" + issue.Key
		}
		views = append(views, view)
	}
	return views
}

// projectLink renders the project an action advances as a link to its row.
//
// Every project has one, whether or not it is in the table above, which is
// what makes this unconditional: the anchor is wherever the project is.
// projectLink renders the project an action advances.
//
// Through the linker rather than built here, so it gets what every other
// reference gets: the title of what it points at, as a tooltip. Hand-building
// the anchor meant this one link — the most-clicked on the page — was the only
// identifier without one, because the tooltip lives in the linker and nothing
// else knew to add it.
func projectLink(id sql.NullString, text *prose) template.HTML {
	if !id.Valid || id.String == "" {
		return "-"
	}
	return text.links.Text(id.String)
}

// age is the short chip on the right of a queue item: what state it is in and
// since when, which is the difference between being patient and nobody having
// looked.
func age(a *store.Action, now string) string {
	if a.SnoozeUntil.Valid {
		if expired(a.SnoozeUntil, now) {
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

// expired reports that a snooze date has arrived or passed.
//
// Against a timestamp, not a date, so this agrees with the queue query: a
// snooze until a bare date is past as soon as that date begins. Comparing
// against the date alone made an item snoozed until today unexpired here and
// expired there — visible in the queue with nothing marking it.
func expired(until sql.NullString, now string) bool {
	return until.Valid && until.String != "" && until.String < now
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

// lateLabel renders how far past its allowance an action is, empty when it is
// not late at all.
//
// Presence in the map is what says late, not the number: a deadline missed an
// hour ago is nought days past it and still missed.
func lateLabel(late map[string]int, id string) string {
	days, ok := late[id]
	switch {
	case !ok:
		return ""
	case days == 0:
		return "late"
	case days == 1:
		return "1 day late"
	default:
		return fmt.Sprintf("%d days late", days)
	}
}

// outstandingOwners is who a pull request still needs: the owners its files
// require, less anyone whose approval already covers them.
//
// A rough subtraction rather than the real one. Reducing properly means
// expanding an approver into the teams they belong to, which is a live read of
// GitHub — see `roz codeowners`. This is the page, so it says the useful half
// without a network call: an owner who has approved under their own name is
// dropped, and one covered only through a team is not.
func outstandingOwners(p *store.PR) []string {
	required := jsonList(p.RequiredOwners)
	if len(required) == 0 {
		return nil
	}
	approved := map[string]bool{}
	for _, login := range jsonList(p.Approvals) {
		approved["@"+login] = true
	}

	out := make([]string, 0, len(required))
	for _, owner := range required {
		if !approved[owner] {
			out = append(out, owner)
		}
	}
	return out
}
