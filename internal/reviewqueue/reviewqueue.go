// Package reviewqueue decides which open pull requests are waiting on a team,
// and on whom each one is waiting.
//
// A team's review requests are not a good guide to either. GitHub adds a
// CODEOWNERS team when a push touches its files and never removes it when a
// later push takes those files back out, so a pull request can sit in a team's
// queue for months over files it no longer changes. And a team named only by a
// broad rule — the fallback, or one listing half the organisation — is
// requested on changes nobody on it needs to see.
//
// So a pull request is the team's when its current diff touches a file whose
// CODEOWNERS rule names the team itself, on a rule narrow enough to mean it,
// or when one of the team's members is asked for by name.
package reviewqueue

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/scottlaird/roz/internal/codeowners"
	"github.com/scottlaird/roz/internal/github"
)

// Config says whose queue to build.
type Config struct {
	// Team is org/slug.
	Team codeowners.Owner
	// Members are the team's logins.
	Members []string
	// MaxRuleOwners ignores CODEOWNERS rules naming more owners than this: a
	// rule listing the team among many is a catch-all, not a claim. Zero
	// means no limit.
	MaxRuleOwners int
	// JiraPrefixes restricts Jira keys to these projects. Empty accepts any
	// key-shaped string that isn't a known standard.
	JiraPrefixes []string
}

// Section groups items by who has the next move.
type Section int

const (
	// NotYetReviewed has had no review or comment from any member of the team
	// other than its author. Nobody on the team has picked it up, which is why
	// it comes first, whoever it is nominally waiting on.
	NotYetReviewed Section = iota
	// OnMember is waiting on a specific member of the team.
	OnMember
	// OnAuthor is waiting on its author to answer a change request.
	OnAuthor
	// OnOthers is waiting on someone outside the team.
	OnOthers
)

// Item is a pull request waiting on the team.
type Item struct {
	PR github.OpenPR
	// Reasons says why it is the team's.
	Reasons []string
	// WaitingOn is who has the next move, and Why says how that was decided.
	WaitingOn []string
	Why       string
	Section   Section
	// Jira is the issue keys the pull request refers to.
	Jira []string
}

// Skipped is a pull request the team is requested on that is not the team's.
// Reported so the filtering can be checked rather than trusted.
type Skipped struct {
	PR     github.OpenPR
	Reason string
}

// Select builds the queue. owners holds each base ref's CODEOWNERS; a ref
// missing from it has none.
func Select(prs []github.OpenPR, owners map[string]*codeowners.File, cfg Config) ([]Item, []Skipped) {
	members := make(map[string]bool, len(cfg.Members))
	for _, m := range cfg.Members {
		members[strings.ToLower(m)] = true
	}

	var items []Item
	var skipped []Skipped
	for _, pr := range prs {
		// Neither is waiting on a reviewer: a draft isn't asking yet, and an
		// approved pull request is its author's to merge.
		if pr.Draft || pr.ReviewDecision == "APPROVED" {
			continue
		}
		reasons := teamReasons(pr, owners[pr.BaseRef], cfg)
		reasons = append(reasons, memberReasons(pr, members)...)
		if len(reasons) == 0 {
			if requested(pr, cfg.Team) {
				skipped = append(skipped, Skipped{PR: pr, Reason: skipReason(pr, owners[pr.BaseRef], cfg)})
			}
			continue
		}
		waitingOn, why := waitingOn(pr, cfg.Team, len(teamReasonsOnly(reasons)) > 0)
		items = append(items, Item{
			PR: pr, Reasons: reasons, WaitingOn: waitingOn, Why: why,
			Section: section(pr, waitingOn, why, cfg.Team, members),
			Jira:    JiraKeys(pr, cfg.JiraPrefixes),
		})
	}

	// Longest-idle first: those are the ones a daily message exists to surface.
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].PR.LastActivity.Before(items[j].PR.LastActivity)
	})
	return items, skipped
}

const teamReasonPrefix = "owns "

// teamReasons names the first changed file the team owns, if any.
func teamReasons(pr github.OpenPR, file *codeowners.File, cfg Config) []string {
	if file == nil {
		return nil
	}
	for _, path := range pr.Files {
		if claims(file, path, cfg) {
			more := ""
			if n := countClaimed(pr, file, cfg) - 1; n > 0 {
				more = fmt.Sprintf(" (+%d more)", n)
			}
			return []string{teamReasonPrefix + path + more}
		}
	}
	return nil
}

func teamReasonsOnly(reasons []string) []string {
	var out []string
	for _, r := range reasons {
		if strings.HasPrefix(r, teamReasonPrefix) {
			out = append(out, r)
		}
	}
	return out
}

func claims(file *codeowners.File, path string, cfg Config) bool {
	owners := file.Owners(path)
	if cfg.MaxRuleOwners > 0 && len(owners) > cfg.MaxRuleOwners {
		return false
	}
	for _, o := range owners {
		if o == cfg.Team {
			return true
		}
	}
	return false
}

func countClaimed(pr github.OpenPR, file *codeowners.File, cfg Config) int {
	n := 0
	for _, path := range pr.Files {
		if claims(file, path, cfg) {
			n++
		}
	}
	return n
}

// memberReasons names members asked for by name. An author assigning
// themselves is not the team being asked for anything.
func memberReasons(pr github.OpenPR, members map[string]bool) []string {
	var reasons []string
	for _, u := range pr.RequestedUsers {
		if members[strings.ToLower(u)] {
			reasons = append(reasons, "review requested from @"+u)
		}
	}
	for _, a := range pr.Assignees {
		if members[strings.ToLower(a)] && !strings.EqualFold(a, pr.Author) {
			reasons = append(reasons, "assigned to @"+a)
		}
	}
	return reasons
}

func requested(pr github.OpenPR, team codeowners.Owner) bool {
	for _, t := range pr.RequestedTeams {
		if codeowners.NormalizeOwner(t) == team {
			return true
		}
	}
	return false
}

func skipReason(pr github.OpenPR, file *codeowners.File, cfg Config) string {
	if file == nil {
		return "requested, but the base branch has no CODEOWNERS"
	}
	for _, path := range pr.Files {
		owners := file.Owners(path)
		for _, o := range owners {
			if o == cfg.Team {
				return fmt.Sprintf("requested only through a %d-owner rule (%s)", len(owners), path)
			}
		}
	}
	return "requested, but nothing in the current diff is the team's"
}

// NoReviewer is the Why of a pull request nobody has been asked to review.
const NoReviewer = "no reviewer requested"

// waitingOn says who has the next move. In order: a change request puts it
// back with the author; outstanding review requests are the reviewers'; a
// pull request nobody has reviewed or been asked to is waiting on a reviewer;
// one that has been reviewed or commented on is back with those people if the
// author moved last, and the author's if not.
//
// The team's own request is dropped when the team does not own anything in
// the diff: that request is stale, and naming it would send people to a pull
// request that does not need them.
func waitingOn(pr github.OpenPR, team codeowners.Owner, teamOwns bool) ([]string, string) {
	if len(pr.ChangesRequestedBy) > 0 {
		changesBy := make([]string, len(pr.ChangesRequestedBy))
		for i, login := range pr.ChangesRequestedBy {
			changesBy[i] = "@" + login
		}
		return []string{"@" + pr.Author}, "changes requested by " + strings.Join(changesBy, ", ")
	}

	var reviewers []string
	for _, u := range pr.RequestedUsers {
		reviewers = append(reviewers, "@"+u)
	}
	for _, t := range pr.RequestedTeams {
		if codeowners.NormalizeOwner(t) == team && !teamOwns {
			continue
		}
		reviewers = append(reviewers, "@"+t)
	}
	if len(reviewers) > 0 {
		return reviewers, "review requested"
	}

	// Nobody asked and nobody has reviewed: it needs a reviewer, and naming
	// whoever pushed last would suggest it has one.
	responded := responders(pr)
	if len(responded) == 0 {
		return []string{"@" + pr.Author}, NoReviewer
	}

	// Reviewed or commented on, with nothing outstanding. If the author moved
	// last, it is back with those who responded; if not, the author has
	// something to answer.
	if strings.EqualFold(pr.LastActor, pr.Author) {
		names := make([]string, len(responded))
		for i, login := range responded {
			names[i] = "@" + login
		}
		return names, UpdatedSinceReview
	}
	return []string{"@" + pr.Author}, AuthorsTurn
}

// The Why of a reviewed pull request with no outstanding review request.
const (
	UpdatedSinceReview = "updated since review"
	AuthorsTurn        = "reviewed, author's turn"
)

// section decides which part of the message an item belongs in. Nothing yet
// from the team puts it first. After that: a change request is the author's
// to answer, even when the author is on the team; a named member makes it
// that member's; the team as a whole or nobody makes it the team's to pick up
// again; anyone else is someone else.
func section(pr github.OpenPR, waitingOn []string, why string, team codeowners.Owner, members map[string]bool) Section {
	if !engaged(pr, members) {
		return NotYetReviewed
	}
	if len(pr.ChangesRequestedBy) > 0 {
		return OnAuthor
	}
	switch why {
	case NoReviewer:
		return NotYetReviewed
	case AuthorsTurn:
		return OnAuthor
	}
	onTeam := false
	for _, w := range waitingOn {
		name := strings.TrimPrefix(w, "@")
		if members[strings.ToLower(name)] {
			return OnMember
		}
		if codeowners.NormalizeOwner(name) == team {
			onTeam = true
		}
	}
	if onTeam {
		return NotYetReviewed
	}
	return OnOthers
}

// engaged reports whether a member other than the author has reviewed or
// commented.
func engaged(pr github.OpenPR, members map[string]bool) bool {
	for _, login := range responders(pr) {
		if members[strings.ToLower(login)] {
			return true
		}
	}
	return false
}

// responders are everyone other than the author who has reviewed or
// commented, reviewers first.
func responders(pr github.OpenPR) []string {
	var out []string
	seen := map[string]bool{}
	add := func(login string) {
		key := strings.ToLower(login)
		if login == "" || strings.EqualFold(login, pr.Author) || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, login)
	}
	for _, r := range pr.Reviews {
		add(r.Author)
	}
	for _, c := range pr.Commenters {
		add(c)
	}
	return out
}

// Jira keys are read from the title, where a bare key is the convention, and
// from issue links in the body. A bare key in the body is too often something
// else to trust, and even in a title a few standards look like keys.
var (
	jiraInTitle = regexp.MustCompile(`\b([A-Z][A-Z0-9]+)-[0-9]+\b`)
	jiraInBody  = regexp.MustCompile(`/browse/(([A-Z][A-Z0-9]+)-[0-9]+)\b`)

	notJira = map[string]bool{
		"AES": true, "CVE": true, "HTTP": true, "ISO": true, "MD": true,
		"RFC": true, "SHA": true, "SSL": true, "TLS": true, "UTF": true,
	}
)

// JiraKeys returns the issue keys a pull request refers to, title first, each
// once. With prefixes, only keys in those projects count: a configured list
// is a better filter than any guess at what isn't a project.
func JiraKeys(pr github.OpenPR, prefixes []string) []string {
	allowed := map[string]bool{}
	for _, p := range prefixes {
		allowed[strings.ToUpper(p)] = true
	}
	wanted := func(project string) bool {
		if len(allowed) > 0 {
			return allowed[project]
		}
		return !notJira[project]
	}

	var keys []string
	seen := map[string]bool{}
	add := func(key, project string) {
		if wanted(project) && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for _, m := range jiraInTitle.FindAllStringSubmatch(pr.Title, -1) {
		add(m[0], m[1])
	}
	for _, m := range jiraInBody.FindAllStringSubmatch(pr.Body, -1) {
		add(m[1], m[2])
	}
	return keys
}

// Options controls rendering.
type Options struct {
	Now time.Time
	// StaleAfter folds pull requests idle longer than this into one line at
	// the end. Zero folds nothing.
	StaleAfter time.Duration
	// ShowReasons adds why each pull request is the team's. Mostly for
	// checking the filter.
	ShowReasons bool
	// JiraURL is the base a key is appended to, as roz's jira_base_url
	// holds it: https://example.atlassian.net/browse. Empty shows keys
	// without links.
	JiraURL string
	// DataTables renders each section as a paginated, sortable data_table
	// block rather than a plain table.
	DataTables bool
	// PageSize is a data table's rows per page. Zero is Slack's default.
	PageSize int
}

func (o Options) jiraLink(key string) string {
	if o.JiraURL == "" {
		return ""
	}
	return strings.TrimRight(o.JiraURL, "/") + "/" + key
}

func titleWithAuthor(pr github.OpenPR) string {
	return fmt.Sprintf("%s (@%s)", pr.Title, pr.Author)
}

// Format renders the queue as Slack mrkdwn: a section per Section, longest
// idle first, with stale pull requests folded into one line at the end so the
// old tail doesn't bury what needs doing today.
func Format(repo string, team codeowners.Owner, items []Item, opts Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*Pull requests for %s in %s* (%d)\n", team.String(), repo, len(items))
	if len(items) == 0 {
		b.WriteString("Nothing waiting.\n")
		return b.String()
	}

	groups, stale := group(team, items, opts)
	for _, g := range groups {
		fmt.Fprintf(&b, "\n*%s* (%d)\n", g.title, len(g.items))
		for _, it := range g.items {
			fmt.Fprintf(&b, "• %s %s", link(it.PR), escape(titleWithAuthor(it.PR)))
			for _, k := range it.Jira {
				if url := opts.jiraLink(k); url != "" {
					fmt.Fprintf(&b, " <%s|%s>", url, k)
				} else {
					fmt.Fprintf(&b, " %s", k)
				}
			}
			fmt.Fprintf(&b, " — waiting on %s (%s) · idle %s",
				strings.Join(it.WaitingOn, ", "), it.Why, age(lastActivity(it.PR), opts.Now))
			if opts.ShowReasons {
				fmt.Fprintf(&b, " · _%s_", strings.Join(it.Reasons, "; "))
			}
			b.WriteString("\n")
		}
	}

	if len(stale) > 0 {
		links := make([]string, len(stale))
		for i, it := range stale {
			links[i] = link(it.PR)
		}
		fmt.Fprintf(&b, "\n_%d idle over %s:_ %s\n", len(stale), days(opts.StaleAfter), strings.Join(links, " "))
	}
	return b.String()
}

type itemGroup struct {
	title string
	items []Item
}

// group splits items into the non-empty sections, in display order, and the
// stale tail.
func group(team codeowners.Owner, items []Item, opts Options) ([]itemGroup, []Item) {
	var stale []Item
	bySection := map[Section][]Item{}
	for _, it := range items {
		if opts.StaleAfter > 0 && opts.Now.Sub(lastActivity(it.PR)) > opts.StaleAfter {
			stale = append(stale, it)
			continue
		}
		bySection[it.Section] = append(bySection[it.Section], it)
	}
	var groups []itemGroup
	for _, s := range []struct {
		section Section
		title   string
	}{
		{NotYetReviewed, "Not yet reviewed by " + team.String()},
		{OnMember, "Waiting on a member of " + team.String()},
		{OnAuthor, "Waiting on the author"},
		{OnOthers, "Waiting on someone else"},
	} {
		if list := bySection[s.section]; len(list) > 0 {
			groups = append(groups, itemGroup{title: s.title, items: list})
		}
	}
	return groups, stale
}

// Blocks renders the queue as a Slack message payload: a header per section
// and a table of its pull requests. text is the fallback notifications show.
func Blocks(repo string, team codeowners.Owner, items []Item, opts Options) map[string]any {
	title := fmt.Sprintf("Pull requests for %s in %s (%d)", team.String(), repo, len(items))
	blocks := []any{
		map[string]any{"type": "header", "text": map[string]any{"type": "plain_text", "text": title}},
	}
	groups, stale := group(team, items, opts)
	if len(items) == 0 {
		blocks = append(blocks, mrkdwnSection("Nothing waiting."))
	}
	for _, g := range groups {
		caption := fmt.Sprintf("%s (%d)", g.title, len(g.items))
		// A data table carries its own caption; a plain table needs a heading.
		if !opts.DataTables {
			blocks = append(blocks, mrkdwnSection("*"+caption+"*"))
		}
		rows := tableRows(g.items, opts)
		if opts.DataTables {
			table := map[string]any{"type": "data_table", "caption": caption, "rows": rows}
			if opts.PageSize > 0 {
				table["page_size"] = opts.PageSize
			}
			blocks = append(blocks, table)
		} else {
			blocks = append(blocks, map[string]any{"type": "table", "column_settings": columnSettings(opts), "rows": rows})
		}
	}
	if len(stale) > 0 {
		links := make([]string, len(stale))
		for i, it := range stale {
			links[i] = link(it.PR)
		}
		blocks = append(blocks, map[string]any{
			"type": "context",
			"elements": []any{map[string]any{"type": "mrkdwn",
				"text": fmt.Sprintf("%d idle over %s: %s", len(stale), days(opts.StaleAfter), strings.Join(links, " "))}},
		})
	}
	return map[string]any{"text": title, "blocks": blocks}
}

// tableRows lays out a section's pull requests, header first.
//
// Slack won't be told how wide to make a column, and it gives them room
// evenly, so every column taken from the title's share shows. The number and
// title therefore share one link cell, and why goes in brackets after who.
func tableRows(items []Item, opts Options) [][]any {
	header := []any{rawCell("Pull request"), rawCell("Jira"), rawCell("Waiting on"), rawCell("Idle")}
	if opts.ShowReasons {
		header = append(header, rawCell("Ours because"))
	}
	rows := [][]any{header}
	for _, it := range items {
		number := it.PR.Key[strings.LastIndex(it.PR.Key, "#"):]
		row := []any{
			linksCell([]cellLink{{url: it.PR.URL, text: number + " " + titleWithAuthor(it.PR)}}),
			jiraCell(it.Jira, opts),
			rawCell(fmt.Sprintf("%s (%s)", strings.Join(it.WaitingOn, ", "), it.Why)),
			idleCell(lastActivity(it.PR), opts),
		}
		if opts.ShowReasons {
			row = append(row, rawCell(strings.Join(it.Reasons, "; ")))
		}
		rows = append(rows, row)
	}
	return rows
}

// columnSettings wraps the plain table's text columns and right-aligns Idle.
func columnSettings(opts Options) []any {
	settings := []any{
		map[string]any{"is_wrapped": true}, map[string]any{"is_wrapped": true},
		map[string]any{"is_wrapped": true}, map[string]any{"align": "right"},
	}
	if opts.ShowReasons {
		settings = append(settings, map[string]any{"is_wrapped": true})
	}
	return settings
}

type cellLink struct{ url, text string }

// idleCell is how long a pull request has sat. In a data table it is a
// number of hours, so the column sorts by age rather than alphabetically
// ("10d" before "2h"), and displays as the same short age a plain table
// shows.
func idleCell(since time.Time, opts Options) map[string]any {
	text := age(since, opts.Now)
	if !opts.DataTables {
		return rawCell(text)
	}
	return map[string]any{"type": "raw_number", "value": int(opts.Now.Sub(since).Hours()), "text": text}
}

// jiraCell links each key when there is a Jira base to link to. A table cell
// can't be empty, so a pull request with no key gets a dash.
func jiraCell(keys []string, opts Options) map[string]any {
	if len(keys) == 0 {
		return rawCell("–")
	}
	if opts.JiraURL == "" {
		return rawCell(strings.Join(keys, ", "))
	}
	links := make([]cellLink, len(keys))
	for i, k := range keys {
		links[i] = cellLink{url: opts.jiraLink(k), text: k}
	}
	return linksCell(links)
}

func mrkdwnSection(text string) map[string]any {
	return map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}
}

func rawCell(text string) map[string]any {
	return map[string]any{"type": "raw_text", "text": text}
}

// linksCell is a rich-text cell of links separated by commas.
func linksCell(links []cellLink) map[string]any {
	var elements []any
	for i, l := range links {
		if i > 0 {
			elements = append(elements, map[string]any{"type": "text", "text": ", "})
		}
		elements = append(elements, map[string]any{"type": "link", "url": l.url, "text": l.text})
	}
	return map[string]any{
		"type":     "rich_text",
		"elements": []any{map[string]any{"type": "rich_text_section", "elements": elements}},
	}
}

func link(pr github.OpenPR) string {
	return fmt.Sprintf("<%s|%s>", pr.URL, pr.Key[strings.LastIndex(pr.Key, "#"):])
}

func lastActivity(pr github.OpenPR) time.Time {
	if pr.LastActivity.IsZero() {
		return pr.CreatedAt
	}
	return pr.LastActivity
}

func days(d time.Duration) string {
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// FormatSkipped renders what was left out, for checking the filter.
func FormatSkipped(skipped []Skipped) string {
	var b strings.Builder
	for _, s := range skipped {
		fmt.Fprintf(&b, "  skipped %s %q: %s\n", s.PR.Key, s.PR.Title, s.Reason)
	}
	return b.String()
}

// escape keeps a title from breaking Slack's link and emphasis syntax.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func age(since, now time.Time) string {
	d := now.Sub(since)
	switch {
	case d >= 48*time.Hour:
		return days(d)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}
