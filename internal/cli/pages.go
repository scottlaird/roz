package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/scottlaird/roz/internal/static"
	"github.com/scottlaird/roz/internal/store"
)

// route says which page is being built: the index, a listing, or one entity.
type route struct {
	kind string
	id   string
	// view is a saved view to build a listing from, named in ?view=. Empty
	// builds the listing's own default, which is what every page did before
	// there was anywhere to say otherwise.
	view string
}

// page is one document roz serves: how it is reached, what draws it, and what
// the shell says about it.
//
// pageTable is the whole list. Adding a page is one entry there and one body
// template; the server registers its path from the entry (pageRoutes), the
// renderer parses its template from the entry (pages), and buildRoute calls
// its draw. Before this a page was a constant, a line in the template map, an
// arm in the breadcrumb switch, a title call, arms in the kind switch and a
// route spelled out in another package -- seven places, and /dependencygraph
// was added by finding all of them.
type page struct {
	kind string
	// pattern is what the server registers, in net/http's pattern syntax;
	// wildcard names the path value that carries the entity's identifier,
	// and is empty for a listing.
	pattern, wildcard string
	// template is the body template's name under templates/.
	template string
	// crumbs is the trail above the page, or nil for the index, which is
	// where every trail leads.
	crumbs func(id string) []crumbView
	// onPage names the entities this document will draw, which decides
	// whether prose linking one anchors within the page or leaves for its
	// own. nil draws none, so every link leaves.
	onPage func(d *pageData) map[string]bool
	// draw fills in what only this page shows, running whatever queries only
	// it needs. The shell -- title, crumbs, notes, the stamp -- is in place
	// when it runs, and text is the prose renderer built for this page.
	draw func(ctx context.Context, st *store.Store, d *pageData, at route, text *prose, content *pageContent) error
}

var home = crumbView{Label: "roz", Href: "/"}

var pageTable = []page{
	{kind: pageIndex, pattern: "/{$}", template: "index",
		onPage: func(d *pageData) map[string]bool {
			on := idSet(append(store.IDs(d.queue), store.IDs(d.waiting)...))
			for _, node := range d.tree {
				on[node.Project.ID] = true
			}
			return on
		},
		draw: drawIndex},
	{kind: pageProjects, pattern: "/projects", template: "projects",
		crumbs: func(string) []crumbView { return []crumbView{home, {Label: "projects"}} },
		onPage: func(d *pageData) map[string]bool { return idSet(store.IDs(d.everyProject)) },
		draw:   drawProjects},
	{kind: pageActions, pattern: "/actions", template: "actions",
		crumbs: func(string) []crumbView { return []crumbView{home, {Label: "actions"}} },
		onPage: func(d *pageData) map[string]bool { return idSet(store.IDs(d.everyAction)) },
		draw:   drawActions},
	{kind: pageGraph, pattern: "/dependencygraph", template: "dependencygraph",
		crumbs: func(string) []crumbView { return []crumbView{home, {Label: "dependencies"}} },
		draw:   drawGraph},
	{kind: pageProject, pattern: "/project/{id}", wildcard: "id", template: "project",
		crumbs: func(id string) []crumbView {
			return []crumbView{home, {Label: "projects", Href: "/projects"}, {Label: id}}
		},
		draw: drawProject},
	{kind: pageAction, pattern: "/action/{id}", wildcard: "id", template: "action",
		crumbs: func(id string) []crumbView {
			return []crumbView{home, {Label: "actions", Href: "/actions"}, {Label: id}}
		},
		draw: drawAction},
}

func pageByKind(kind string) (page, bool) {
	for _, p := range pageTable {
		if p.kind == kind {
			return p, true
		}
	}
	return page{}, false
}

func idSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// pageData is what every page needs before it knows which page it is: the
// identifiers prose can link, the rows those links need to land on, the
// verbs and joins every row is drawn with, and what the shell prints on
// every page -- the note slots and the stamp's three counts.
//
// Larger than "the identifiers" because the shell draws the queue, waiting
// and open-project counts at the foot of every page and a note slot at the
// top of each; the queries only the index needs -- the calendar, the open
// actions per project, the dependency graph -- are in its draw, and so on
// for the others. Every lookup here is batched: a per-row query would be
// invisible at this size and wrong at any other, and the shape is the thing
// that gets copied.
type pageData struct {
	now   time.Time
	today string
	// stamp is the instant the queries compared against. A calendar window
	// is a span of days and asks about the date; a snooze is compared against
	// the instant. See Action.SnoozeExpired.
	stamp string
	cfg   settings

	// Everything tracked, not only what a page happens to draw: prose can
	// reference a pull request no action on the page is about, and an
	// identifier only links if it exists.
	allPRs       []*store.PR
	allIssues    []*store.TrackerIssue
	shortNames   map[string]string
	everyAction  []*store.Action
	everyProject []*store.Project
	notes        map[string]*store.PageNote

	// Unblocked is the queue. Waiting is what the queue leaves out *because
	// it is waiting on someone* -- ready, unhidden, and a wait verb. Blocked
	// and hidden actions stay out of both: their blocker is already in the
	// queue, and listing them is the noise the fold rule exists to stop.
	//
	// Neither is a view, and neither can be: both read the verb's rank class
	// through actionverb and whether the action's project is blocked, and
	// neither is a column of the action. A ranking is not a predicate -- the
	// same conclusion #169 reached about --sort.
	queue   []*store.Action
	waiting []*store.Action
	// projects is the live work -- open rather than active, since a blocked
	// project is live and hiding it is how one sat unseen for a session --
	// read from the open_projects view where there is one, so that what the
	// page means by live work is a row somebody can edit. tree is the same
	// projects with any closed parent above open work pulled back in, so the
	// hierarchy has no gaps.
	projects []*store.Project
	tree     []store.TreeNode

	issuesByProject map[string][]*store.TrackerIssue
	prsByAction     map[string][]store.ActionPR
	verbs           []*store.ActionVerb
	rank            map[string]string
	late            map[string]int
}

func loadPageData(ctx context.Context, st *store.Store, now time.Time, cfg settings) (*pageData, error) {
	d := &pageData{
		now:   now,
		today: now.UTC().Format(store.DateFormat),
		stamp: now.UTC().Format(store.TimeFormat),
		cfg:   cfg,
	}
	var err error
	if d.allPRs, err = st.ListPRs(ctx, store.PRFilter{}); err != nil {
		return nil, err
	}
	if d.allIssues, err = st.ListTrackerIssues(ctx, store.IssueFilter{}); err != nil {
		return nil, err
	}
	if d.shortNames, err = st.ShortNames(ctx); err != nil {
		return nil, err
	}
	if d.notes, err = st.PageNotes(ctx); err != nil {
		return nil, err
	}
	if d.everyAction, err = st.ListActions(ctx, store.ActionFilter{}); err != nil {
		return nil, err
	}
	if d.everyProject, err = st.ListProjects(ctx, store.ProjectFilter{}); err != nil {
		return nil, err
	}
	if d.queue, err = st.ListActions(ctx, store.ActionFilter{Unblocked: true, Order: store.OrderPriority}); err != nil {
		return nil, err
	}
	// The same definition the CLI's --waiting uses, rather than a second one
	// here: a queue and the things it deliberately omits should not be able
	// to disagree about what is in play.
	if d.waiting, err = st.ListActions(ctx, store.ActionFilter{Waiting: true, Order: store.OrderPriority}); err != nil {
		return nil, err
	}
	projectFilter := store.ProjectFilter{Open: true, Order: store.OrderPriority}
	if v, ok := savedFilter(ctx, st, "open_projects", projectColumns); ok {
		projectFilter = store.ProjectFilter{
			Where: v.where, WhereArgs: v.args, Order: v.ranking, Sort: v.order,
		}
	}
	if d.projects, err = st.ListProjects(ctx, projectFilter); err != nil {
		return nil, err
	}
	connected, err := st.WithAncestors(ctx, d.projects)
	if err != nil {
		return nil, err
	}
	d.tree = store.Tree(connected)
	if d.issuesByProject, err = st.IssuesByProject(ctx); err != nil {
		return nil, err
	}
	if d.prsByAction, err = st.PRsByAction(ctx); err != nil {
		return nil, err
	}
	if d.verbs, err = st.ListVerbs(ctx, false, store.Sort{}, store.SQLWhere{}); err != nil {
		return nil, err
	}
	d.rank = map[string]string{}
	for _, v := range d.verbs {
		d.rank[v.Verb] = v.RankClass
	}
	if d.late, err = st.LateActions(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// prose is the renderer for one page, which needs to know which entities
// that page draws: a link to one of them is an anchor, and to anything else
// a trip to its own page.
func (d *pageData) prose(onPage map[string]bool) *prose {
	return newProse(d.cfg.jiraBase, d.cfg.jiraPrefixes, d.shortNames,
		refsFor(d.everyAction, d.everyProject, onPage),
		titlesFrom(d.allPRs, d.allIssues, d.everyAction, d.everyProject))
}

// shell is the part of every page that is the same page: the heading, the
// trail, the note slots and the stamp. A draw overrides the title where the
// page has a better one.
func (d *pageData) shell(live bool, text *prose, pg page, at route) *pageContent {
	content := &pageContent{
		Owner: d.cfg.owner,
		Title: titleFor(d.cfg.owner),
		GeneratedAt: whenView{
			At:   d.now.UTC().Format(time.RFC3339),
			Text: d.now.UTC().Format(time.RFC3339),
		},
		Live:    live,
		Favicon: favicon,
	}
	content.StylesheetURL = static.URL(stylesheet)
	if pg.crumbs != nil {
		content.Crumbs = pg.crumbs(at.id)
	}
	// Rendered through the linker, so a note naming SL7 links it the way a
	// project summary does.
	for slot, note := range d.notes {
		if strings.TrimSpace(note.Body) == "" {
			continue // an emptied slot draws nothing, not an empty box
		}
		if content.Notes == nil {
			content.Notes = map[string]noteView{}
		}
		content.Notes[slot] = noteView{
			Body: text.markdown.Render(note.Body),
			Set: whenView{
				At:   note.UpdatedAt,
				Text: shortDate(note.UpdatedAt),
			},
		}
	}
	content.Stamp = fmt.Sprintf("%d in the queue · %d waiting · %d open projects",
		len(d.queue), len(d.waiting), len(d.tree))
	return content
}

// openPerProject counts the open actions on each project, for the rows that
// show it. Read from the open_actions view where there is one, so the count
// means what the page's definition of open means.
func (d *pageData) openPerProject(ctx context.Context, st *store.Store) (map[string]int, error) {
	openFilter := store.ActionFilter{Open: true, Order: store.OrderPriority}
	if v, ok := savedFilter(ctx, st, "open_actions", actionColumns); ok {
		openFilter = store.ActionFilter{
			Where: v.where, WhereArgs: v.args, Order: v.ranking, Sort: v.order,
		}
	}
	open, err := st.ListActions(ctx, openFilter)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, a := range open {
		if a.ProjectID.Valid {
			counts[a.ProjectID.String]++
		}
	}
	return counts, nil
}

// graph is the dependency diagram, built from everything rather than from the
// filtered listings: a dependency is a fact about the queue, not about
// whichever view is in force.
func (d *pageData) graph(ctx context.Context, st *store.Store) (*dependencyGraph, error) {
	// The other way round from shortNames: prose resolves what a person
	// typed, and the diagram has the repository and wants what to call it.
	repoShort := make(map[string]string, len(d.shortNames))
	for short, repo := range d.shortNames {
		repoShort[repo] = short
	}
	return buildGraph(ctx, st, d.everyProject, d.everyAction, d.queue, d.verbs, repoShort, d.late)
}

func (d *pageData) actionRow(a *store.Action, text *prose) actionView {
	return actionRow(a, d.rank, d.prsByAction, d.issuesByProject, text, d.stamp, d.late)
}

// buildRoute assembles everything the template needs for one page: the loads
// every page shares, then the page's own.
func buildRoute(ctx context.Context, st *store.Store, now time.Time, live bool, cfg settings, at route) (*pageContent, error) {
	pg, ok := pageByKind(at.kind)
	if !ok {
		return nil, errNoSuchPage
	}
	d, err := loadPageData(ctx, st, now, cfg)
	if err != nil {
		return nil, err
	}
	var onPage map[string]bool
	if pg.onPage != nil {
		onPage = pg.onPage(d)
	}
	text := d.prose(onPage)
	content := d.shell(live, text, pg, at)
	if err := pg.draw(ctx, st, d, at, text, content); err != nil {
		return nil, err
	}
	return content, nil
}

func drawIndex(ctx context.Context, st *store.Store, d *pageData, _ route, text *prose, content *pageContent) error {
	windows, err := st.ListCalendarWindows(ctx, store.WindowFilter{
		Upcoming: true,
		Through:  d.now.Add(horizon).UTC().Format(store.DateFormat),
	})
	if err != nil {
		return err
	}
	for _, w := range windows {
		content.Windows = append(content.Windows, windowView{
			Label:    w.Label,
			Kind:     w.Kind,
			Range:    windowRange(w),
			Capacity: w.Capacity,
			Current:  w.Covers(d.today),
		})
	}

	for _, a := range d.queue {
		content.Queue = append(content.Queue, d.actionRow(a, text))
	}
	for _, a := range d.waiting {
		content.Waiting = append(content.Waiting, d.actionRow(a, text))
	}
	content.QueueKey = queueKey(content.Queue, content.Waiting)

	openPerProject, err := d.openPerProject(ctx, st)
	if err != nil {
		return err
	}
	// Drawn as a hierarchy. The order within each level is the one the sort
	// chose: a hierarchy is a way of reading the list, not a reranking. No
	// Href: the index anchors, because the row is already in the document.
	for _, node := range d.tree {
		p := node.Project
		content.Projects = append(content.Projects, projectView{
			Depth:    node.Depth,
			Context:  node.Context,
			ID:       p.ID,
			Title:    text.links.Text(p.Title),
			Summary:  text.markdown.Render(p.Summary),
			Status:   p.Status,
			Priority: nullIntText(p.Priority),
			Effort:   nullText(p.Effort),
			Snooze:   nullText(p.SnoozeUntil),
			Actions:  openPerProject[p.ID],
			Issues:   issueViews(d.issuesByProject[p.ID], text),
			Expired:  p.SnoozeExpired(d.stamp),
		})
	}

	content.Graph, err = d.graph(ctx, st)
	return err
}

func drawGraph(ctx context.Context, st *store.Store, d *pageData, _ route, _ *prose, content *pageContent) error {
	content.Title = titleFor(d.cfg.owner, "dependencies")
	var err error
	content.Graph, err = d.graph(ctx, st)
	return err
}

func drawProjects(ctx context.Context, st *store.Store, d *pageData, at route, text *prose, content *pageContent) error {
	openPerProject, err := d.openPerProject(ctx, st)
	if err != nil {
		return err
	}
	// The hierarchy is what the page shows by default and a view is not: a
	// view is an answer to a question, and indenting a project that survived
	// it under a parent that did not would draw a tree that is not there. So
	// the rows come back flat when a view is in force, which is also what the
	// CLI does with --filter.
	listed, err := listedRows(ctx, st, at.view, "project", projectColumns,
		store.Tree(d.everyProject),
		func(where string, args []any, ranking string, order store.Sort) ([]store.TreeNode, error) {
			rows, err := st.ListProjects(ctx, store.ProjectFilter{
				Where: where, WhereArgs: args, Order: ranking, Sort: order,
			})
			if err != nil {
				return nil, err
			}
			flat := make([]store.TreeNode, 0, len(rows))
			for _, p := range rows {
				flat = append(flat, store.TreeNode{Project: p})
			}
			return flat, nil
		})
	if err != nil {
		return err
	}
	content.ViewLinks = viewLinksFor(ctx, st, "project", "/projects", at.view)
	content.Title = titleFor(d.cfg.owner, "projects", at.view)
	for _, node := range listed {
		content.AllProjects = append(content.AllProjects,
			projectRow(node.Project, node, openPerProject, d.issuesByProject, text, d.stamp))
	}
	return nil
}

func drawActions(ctx context.Context, st *store.Store, d *pageData, at route, text *prose, content *pageContent) error {
	listed, err := listedRows(ctx, st, at.view, "action", actionColumns, d.everyAction,
		func(where string, args []any, ranking string, order store.Sort) ([]*store.Action, error) {
			return st.ListActions(ctx, store.ActionFilter{
				Where: where, WhereArgs: args, Order: ranking, Sort: order,
			})
		})
	if err != nil {
		return err
	}
	content.ViewLinks = viewLinksFor(ctx, st, "action", "/actions", at.view)
	content.Title = titleFor(d.cfg.owner, "actions", at.view)
	for _, a := range listed {
		row := d.actionRow(a, text)
		row.Href = actionHref(a.ID)
		content.AllActions = append(content.AllActions, row)
	}
	return nil
}

func drawProject(ctx context.Context, st *store.Store, d *pageData, at route, text *prose, content *pageContent) error {
	openPerProject, err := d.openPerProject(ctx, st)
	if err != nil {
		return err
	}
	found := false
	for _, p := range d.everyProject {
		switch {
		case p.ID == at.id:
			row := projectRow(p, store.TreeNode{Project: p}, openPerProject, d.issuesByProject, text, d.stamp)
			content.Project, found = &row, true
			// The stored title, not the row's: the row's has been through
			// the prose renderer and carries markup a title bar cannot use.
			content.Title = titleFor(d.cfg.owner, p.ID, p.Title)
		case p.ParentID.Valid && p.ParentID.String == at.id:
			content.Children = append(content.Children,
				projectRow(p, store.TreeNode{Project: p}, openPerProject, d.issuesByProject, text, d.stamp))
		}
	}
	if !found {
		return errNoSuchPage
	}
	for _, a := range d.everyAction {
		if a.ProjectID.Valid && a.ProjectID.String == at.id {
			row := d.actionRow(a, text)
			row.Href = actionHref(a.ID)
			content.TotalActions++
			if !row.Closed {
				content.OpenActions++
			}
			content.OwnActions = append(content.OwnActions, row)
		}
	}
	return nil
}

func drawAction(_ context.Context, _ *store.Store, d *pageData, at route, text *prose, content *pageContent) error {
	for _, a := range d.everyAction {
		if a.ID == at.id {
			row := d.actionRow(a, text)
			content.Action = &row
			content.Title = titleFor(d.cfg.owner, a.ID, a.Title)
		}
	}
	if content.Action == nil {
		return errNoSuchPage
	}
	return nil
}
