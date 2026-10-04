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
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/scottlaird/roz/codeowners"
	"github.com/scottlaird/roz/github"
)

// Config says whose queue to build.
type Config struct {
	// Team is org/slug.
	Team codeowners.Owner
	// Members are the team's logins.
	Members []string
	// TeamMembers is the membership of the teams CODEOWNERS names, keyed by
	// org/slug, for working out which approvals are still needed. Without it
	// a nested team isn't credited for the team it sits inside.
	TeamMembers map[string][]string
	// MaxRuleOwners ignores CODEOWNERS rules naming more owners than this: a
	// rule listing the team among many is a catch-all, not a claim. Zero
	// means no limit.
	MaxRuleOwners int
	// JiraPrefixes restricts Jira keys to these projects. Empty accepts any
	// key-shaped string that isn't a known standard.
	JiraPrefixes []string
	// Branches is every open pull request in the repository, drafts and
	// approved ones included, for finding stacks. Nil means no stack
	// checking.
	Branches []github.PRBranch
	// IgnoreTeams are never named as waited on. GitHub doesn't withdraw a
	// team's review request when whatever asked for it -- a gate that has
	// since been retired, say -- stops needing it.
	IgnoreTeams []codeowners.Owner
	// IncludeMemberAuthored also selects pull requests the team only wrote,
	// for the member_authored section. They are marked AuthoredOnly, and no
	// other section shows them.
	IncludeMemberAuthored bool
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
	// StackedOn is the chain of open pull requests this one is built on,
	// nearest first. Empty when it targets a branch no open pull request
	// owns.
	StackedOn []github.PRBranch
	// Held is set when anything in StackedOn is not yet approved. Reviewing
	// it now would be reviewing code that may still change underneath it.
	Held bool
	// Blocks is the held pull requests anywhere above this one in its stack.
	// Set only on pull requests that aren't approved themselves: those are
	// what the stack is waiting for.
	Blocks []github.OpenPR
	// ByMember is set when a member of the team wrote it.
	ByMember bool
	// AuthoredOnly is set when writing it is the team's only claim: selected
	// only through Config.IncludeMemberAuthored.
	AuthoredOnly bool
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
	members := memberSet(cfg.Members)

	byHead := make(map[string]github.PRBranch, len(cfg.Branches))
	for _, b := range cfg.Branches {
		byHead[b.HeadRef] = b
	}

	var items []Item
	var skipped []Skipped
	for _, pr := range prs {
		// Neither is waiting on a reviewer: a draft isn't asking yet, and an
		// approved pull request is its author's to merge.
		if pr.Draft || pr.ReviewDecision == "APPROVED" {
			continue
		}
		otherOwners := askedForAnotherOwner(pr, owners[pr.BaseRef], cfg)
		reasons := teamReasons(pr, owners[pr.BaseRef], cfg)
		reasons = append(reasons, memberReasons(pr, members, otherOwners)...)
		engagedOnly := false
		if len(reasons) == 0 {
			reasons = engagementReasons(pr, members, otherOwners)
			engagedOnly = len(reasons) > 0
		}
		byMember := members[strings.ToLower(pr.Author)]
		authoredOnly := false
		if len(reasons) == 0 && byMember && cfg.IncludeMemberAuthored {
			reasons = []string{"written by @" + pr.Author}
			authoredOnly = true
		}
		if len(reasons) == 0 {
			if requested(pr, cfg.Team) {
				skipped = append(skipped, Skipped{PR: pr, Reason: skipReason(pr, owners[pr.BaseRef], cfg)})
			}
			continue
		}
		needed := neededOwners(pr, owners[pr.BaseRef], cfg)
		waitingOn, why := waitingOn(pr, cfg.Team, len(teamReasonsOnly(reasons)) > 0, engagedOnly, needed, namedTeams(owners[pr.BaseRef]), cfg)
		items = append(items, Item{
			PR: pr, Reasons: reasons, WaitingOn: waitingOn, Why: why,
			Section:  section(pr, waitingOn, why, cfg.Team, members),
			Jira:     JiraKeys(pr, cfg.JiraPrefixes),
			ByMember: byMember, AuthoredOnly: authoredOnly,
		})
		it := &items[len(items)-1]
		it.StackedOn = stackOf(pr, byHead)
		for _, base := range it.StackedOn {
			if base.ReviewDecision != "APPROVED" {
				it.Held = true
			}
		}
	}

	markBlockers(items)

	// Longest-idle first: those are the ones a daily message exists to surface.
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].PR.LastActivity.Before(items[j].PR.LastActivity)
	})
	return items, skipped
}

// markBlockers records, on each unapproved pull request in the queue, the
// held pull requests stacked above it. A base outside the queue -- another
// team's, or a draft -- has no row to mark, and shows only in the held line.
func markBlockers(items []Item) {
	index := make(map[string]int, len(items))
	for i, it := range items {
		index[it.PR.Key] = i
	}
	for _, it := range items {
		if !it.Held {
			continue
		}
		for _, base := range it.StackedOn {
			if base.ReviewDecision == "APPROVED" {
				continue
			}
			if i, ok := index[base.Key]; ok {
				items[i].Blocks = append(items[i].Blocks, it.PR)
			}
		}
	}
	for i := range items {
		sort.Slice(items[i].Blocks, func(a, b int) bool {
			return items[i].Blocks[a].Key < items[i].Blocks[b].Key
		})
	}
}

// neededOwners is the fewest CODEOWNERS approvals still outstanding, given the
// approvals the pull request already has. Nil when the base branch has no
// CODEOWNERS to plan from.
func neededOwners(pr github.OpenPR, file *codeowners.File, cfg Config) []codeowners.Owner {
	if file == nil {
		return nil
	}
	o := file.Of(pr.Files)
	approved := codeowners.Approval(pr.Approvers, codeowners.NewStaticTeams(cfg.TeamMembers))
	return o.Plan(approved, codeowners.NewStaticMembership(cfg.TeamMembers))
}

// namedTeams is every team a CODEOWNERS file names anywhere.
func namedTeams(file *codeowners.File) codeowners.OwnerSet {
	named := codeowners.OwnerSet{}
	if file != nil {
		named.Add(file.Teams()...)
	}
	return named
}

// maxStackDepth bounds the walk down a stack, against a cycle of branches
// that GitHub would not normally allow but that nothing here should loop on.
const maxStackDepth = 20

// stackOf follows a pull request's base branch down through the open pull
// requests whose head branches they are, nearest first. It stops at a branch
// no open pull request owns: the default branch, a release branch, or a base
// that has already merged.
func stackOf(pr github.OpenPR, byHead map[string]github.PRBranch) []github.PRBranch {
	var chain []github.PRBranch
	seen := map[string]bool{pr.HeadRef: true}
	for base := pr.BaseRef; len(chain) < maxStackDepth && !seen[base]; {
		seen[base] = true
		parent, ok := byHead[base]
		if !ok {
			break
		}
		chain = append(chain, parent)
		base = parent.BaseRef
	}
	return chain
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
//
// A member who also belongs to another team owning files in the diff was most likely asked as that
// team's reviewer, so the request doesn't make the pull request this team's. Without that, a team
// nested inside another would collect every request that names a member the two share.
func memberReasons(pr github.OpenPR, members, otherOwners map[string]bool) []string {
	var reasons []string
	ours := func(login string) bool {
		key := strings.ToLower(login)
		return members[key] && !otherOwners[key]
	}
	for _, u := range pr.RequestedUsers {
		if ours(u) {
			reasons = append(reasons, "review requested from @"+u)
		}
	}
	for _, a := range pr.Assignees {
		if ours(a) && !strings.EqualFold(a, pr.Author) {
			reasons = append(reasons, "assigned to @"+a)
		}
	}
	return reasons
}

// askedForAnotherOwner is the logins, lower-cased, of every member of a team other than ours that
// owns a file in the diff. Catch-all rules don't count here any more than they claim a pull
// request for the team: a team named among many owns nothing in particular.
func askedForAnotherOwner(pr github.OpenPR, file *codeowners.File, cfg Config) map[string]bool {
	logins := map[string]bool{}
	if file == nil {
		return logins
	}
	for _, path := range pr.Files {
		owners := file.Owners(path)
		if cfg.MaxRuleOwners > 0 && len(owners) > cfg.MaxRuleOwners {
			continue
		}
		for _, o := range owners {
			if o == cfg.Team || !o.IsTeam() {
				continue
			}
			for _, login := range cfg.TeamMembers[string(o)] {
				logins[strings.ToLower(login)] = true
			}
		}
	}
	return logins
}

// engagementReasons names members who have reviewed or commented, for a pull request nothing else
// makes the team's: a member in the middle of a review has a stake in it whoever owns the files.
//
// As with a request by name, engagement by a member who also belongs to a team owning files in
// the diff is that team's -- unless the author is a member too. A conversation between two
// members is the team's even on files a parent team owns; without that, nesting credits every
// such review to the parent.
func engagementReasons(pr github.OpenPR, members, otherOwners map[string]bool) []string {
	authorIsMember := members[strings.ToLower(pr.Author)]
	var reasons []string
	for _, login := range responders(pr) {
		key := strings.ToLower(login)
		if members[key] && (!otherOwners[key] || authorIsMember) {
			reasons = append(reasons, "@"+login+" reviewed or commented")
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

// outstandingReviewers says who a pull request with review requests is
// waiting on. People asked by name always count. Teams count only where
// CODEOWNERS still needs them: GitHub requests every owning team, never takes
// a request back, and doesn't know that one approval can satisfy several
// teams, so its request list overstates what's left. A needed team is left
// out when someone asked by name already stands for it.
//
// A requested team that no CODEOWNERS rule names always counts: something
// other than CODEOWNERS asked for it, and nothing here knows whether that is
// satisfied.
//
// Without CODEOWNERS to plan from, or when the plan is satisfied but GitHub
// still wants reviews (a required count above one, say), this falls back to
// the requested teams, minus the team's own request when it owns nothing.
func outstandingReviewers(pr github.OpenPR, team codeowners.Owner, teamOwns bool, needed []codeowners.Owner, named codeowners.OwnerSet, cfg Config) []string {
	var reviewers []string
	seen := map[codeowners.Owner]bool{}
	for _, o := range cfg.IgnoreTeams {
		seen[o] = true
	}
	add := func(o codeowners.Owner, display string) {
		if !seen[o] {
			seen[o] = true
			reviewers = append(reviewers, "@"+display)
		}
	}
	// Logins keep the capitalization GitHub gives them; teams are shown as
	// CODEOWNERS normalizes them.
	requestedUsers := stillAwaited(pr)
	for _, u := range requestedUsers {
		add(codeowners.NormalizeOwner(u), u)
	}

	if len(needed) > 0 {
		covered := codeowners.Approval(requestedUsers, codeowners.NewStaticTeams(cfg.TeamMembers))
		for _, o := range needed {
			if !covered.Contains(o) {
				add(o, string(o))
			}
		}
		// A team no CODEOWNERS rule names was asked for some other reason --
		// by hand, or by a branch ruleset such as a deployment gate. Nothing
		// here can tell whether it is still needed, so it stays.
		for _, t := range pr.RequestedTeams {
			if o := codeowners.NormalizeOwner(t); !named.Contains(o) {
				add(o, string(o))
			}
		}
		return reviewers
	}

	for _, t := range pr.RequestedTeams {
		o := codeowners.NormalizeOwner(t)
		if o == team && !teamOwns {
			continue
		}
		add(o, string(o))
	}
	return reviewers
}

// stillAwaited is the requested reviewers, less whoever made the most recent move. GitHub keeps a
// request open through a reviewer's comments -- only a submitted review clears it -- so a reviewer
// who asked a question last is still listed as requested while the answer is the author's to give.
func stillAwaited(pr github.OpenPR) []string {
	if pr.LastActor == "" || strings.EqualFold(pr.LastActor, pr.Author) {
		return pr.RequestedUsers
	}
	var out []string
	for _, u := range pr.RequestedUsers {
		if !strings.EqualFold(u, pr.LastActor) {
			out = append(out, u)
		}
	}
	return out
}

// NoReviewer is the Why of a pull request nobody has been asked to review.
const NoReviewer = "no reviewer requested"

// waitingOn says who has the next move. In order: a change request puts it
// back with the author; outstanding review requests are the reviewers'; a
// pull request nobody has reviewed or been asked to is waiting on a reviewer;
// one that has been reviewed or commented on is back with those people if the
// author moved last, and the author's if not.
//
// A pull request that is the team's only because a member engaged with it is decided the same
// way, the team's part being that member's conversation with the author.
//
// Once a member has reviewed or commented, the team's own request is decided
// the same way: that member is the team's reviewer, and the open request says
// nothing about whose turn it is. Anyone else still requested is listed after.
//
// The team's own request is dropped when the team does not own anything in
// the diff: that request is stale, and naming it would send people to a pull
// request that does not need them.
func waitingOn(pr github.OpenPR, team codeowners.Owner, teamOwns, engagedOnly bool, needed []codeowners.Owner, named codeowners.OwnerSet, cfg Config) ([]string, string) {
	if len(pr.ChangesRequestedBy) > 0 {
		changesBy := make([]string, len(pr.ChangesRequestedBy))
		for i, login := range pr.ChangesRequestedBy {
			changesBy[i] = "@" + login
		}
		return []string{"@" + pr.Author}, "changes requested by " + strings.Join(changesBy, ", ")
	}

	if len(pr.RequestedUsers) > 0 || len(pr.RequestedTeams) > 0 {
		reviewers := outstandingReviewers(pr, team, teamOwns, needed, named, cfg)
		others := make([]string, 0, len(reviewers))
		for _, r := range reviewers {
			if codeowners.NormalizeOwner(r) != team {
				others = append(others, r)
			}
		}
		if (len(others) < len(reviewers) || engagedOnly) && engaged(pr, memberSet(cfg.Members)) {
			names, why := turn(pr, responders(pr))
			return append(names, others...), why
		}
		if len(reviewers) > 0 {
			return reviewers, "review requested"
		}
	}

	// Nobody asked and nobody has reviewed: it needs a reviewer, and naming
	// whoever pushed last would suggest it has one.
	responded := responders(pr)
	if len(responded) == 0 {
		return []string{"@" + pr.Author}, NoReviewer
	}

	return turn(pr, responded)
}

// turn says whose move a reviewed or commented-on pull request is: back with
// those who responded if the author moved last, the author's if not.
func turn(pr github.OpenPR, responded []string) ([]string, string) {
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

// memberSet keys logins by their lower case.
func memberSet(logins []string) map[string]bool {
	members := make(map[string]bool, len(logins))
	for _, m := range logins {
		members[strings.ToLower(m)] = true
	}
	return members
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
	// Sections names the sections to show, in order; see SectionNames. Each
	// pull request goes in the first that matches, and one that matches none
	// is left out. Empty means DefaultSections.
	Sections []string
	// SLOWarn and SLOBreach mark a pull request idle longer than each with a yellow or red
	// circle beside its idle time. Zero leaves that mark off.
	SLOWarn, SLOBreach time.Duration
}

// sloMark is the circle, with a trailing space, for a pull request idle for d; empty within both
// thresholds.
func (o Options) sloMark(d time.Duration) string {
	switch {
	case o.SLOBreach > 0 && d > o.SLOBreach:
		return "🔴 "
	case o.SLOWarn > 0 && d > o.SLOWarn:
		return "🟡 "
	default:
		return ""
	}
}

// idleText is a pull request's idle time with its SLO mark.
func (o Options) idleText(since time.Time) string {
	return o.sloMark(o.Now.Sub(since)) + age(since, o.Now)
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
	groups, stale, held := group(team, items, opts)
	var b strings.Builder
	fmt.Fprintf(&b, "*Pull requests for %s in %s* (%d)\n", team.String(), repo, shown(groups, stale, held))
	if len(groups)+len(stale)+len(held) == 0 {
		b.WriteString("Nothing waiting.\n")
		return b.String()
	}

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
			since := lastActivity(it.PR)
			fmt.Fprintf(&b, " — waiting on %s (%s) · %sidle %s",
				strings.Join(it.WaitingOn, ", "), it.Why, opts.sloMark(opts.Now.Sub(since)), age(since, opts.Now))
			if len(it.Blocks) > 0 {
				blocked := make([]string, len(it.Blocks))
				for i, pr := range it.Blocks {
					blocked[i] = link(pr)
				}
				fmt.Fprintf(&b, " · *blocks %s*", strings.Join(blocked, ", "))
			}
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
	if len(held) > 0 {
		fmt.Fprintf(&b, "\n_%d waiting on their base pull requests:_ %s\n", len(held), heldList(held))
	}
	return b.String()
}

// heldList names each held pull request and the unapproved base it waits on,
// the nearest one: that is the one to review first.
func heldList(held []Item) string {
	parts := make([]string, len(held))
	for i, it := range held {
		parts[i] = link(it.PR)
		for _, base := range it.StackedOn {
			if base.ReviewDecision != "APPROVED" {
				parts[i] += fmt.Sprintf(" (on <%s|#%d>)", base.URL, base.Number)
				break
			}
		}
	}
	return strings.Join(parts, ", ")
}

type itemGroup struct {
	title string
	items []Item
}

// Section names, for Options.Sections.
const (
	SectionTeamUnreviewed = "team_unreviewed"
	SectionMemberWaiting  = "member_waiting"
	SectionMemberAuthor   = "member_author"
	SectionOthers         = "others"
	SectionMemberAuthored = "member_authored"
	SectionStacked        = "stacked"
	SectionStale          = "stale"
)

// DefaultSections is what a queue shows when Options.Sections is empty.
var DefaultSections = []string{
	SectionTeamUnreviewed, SectionMemberWaiting, SectionMemberAuthor, SectionOthers,
	SectionStacked, SectionStale,
}

// sectionDef is one section: what it holds and what it is called.
type sectionDef struct {
	name, description string
	// title is the heading; folded sections are one line at the end instead.
	title  func(team codeowners.Owner) string
	folded bool
	match  func(it Item, opts Options) bool
}

// teamSection matches a pull request the team claims in one of the waiting-on
// sections. One selected only because a member wrote it is not the team's to
// review, so it stays out of all of them.
func teamSection(s Section) func(Item, Options) bool {
	return func(it Item, _ Options) bool { return !it.AuthoredOnly && it.Section == s }
}

var sectionDefs = []sectionDef{
	{name: SectionTeamUnreviewed, description: "the team's, with no review or comment from any member yet",
		title: func(t codeowners.Owner) string { return "Not yet reviewed by " + t.String() },
		match: teamSection(NotYetReviewed)},
	{name: SectionMemberWaiting, description: "the team's, waiting on a specific member",
		title: func(t codeowners.Owner) string { return "Waiting on a member of " + t.String() },
		match: teamSection(OnMember)},
	{name: SectionMemberAuthor, description: "the team's, waiting on its author after a review or change request",
		title: func(codeowners.Owner) string { return "Waiting on the author" },
		match: teamSection(OnAuthor)},
	{name: SectionOthers, description: "the team's, waiting on someone outside the team",
		title: func(codeowners.Owner) string { return "Waiting on someone else" },
		match: teamSection(OnOthers)},
	{name: SectionMemberAuthored, description: "written by a member, whoever it is waiting on",
		title: func(t codeowners.Owner) string { return "Written by members of " + t.String() },
		match: func(it Item, _ Options) bool { return it.ByMember }},
	{name: SectionStacked, description: "held until the pull requests it is stacked on are approved; one line at the end",
		folded: true, match: func(it Item, _ Options) bool { return it.Held }},
	{name: SectionStale, description: "idle longer than the stale-after time; one line at the end",
		folded: true, match: func(it Item, opts Options) bool {
			return opts.StaleAfter > 0 && opts.Now.Sub(lastActivity(it.PR)) > opts.StaleAfter
		}},
}

// SectionInfo describes a section, for listing the choices.
type SectionInfo struct {
	Name, Description string
}

// SectionNames lists every section, in the default order.
func SectionNames() []SectionInfo {
	out := make([]SectionInfo, len(sectionDefs))
	for i, d := range sectionDefs {
		out[i] = SectionInfo{Name: d.name, Description: d.description}
	}
	return out
}

// ValidateSections checks a list of section names.
func ValidateSections(names []string) error {
	seen := map[string]bool{}
	for _, n := range names {
		if _, ok := sectionByName(n); !ok {
			return fmt.Errorf("unknown section %q", n)
		}
		if seen[n] {
			return fmt.Errorf("section %q listed twice", n)
		}
		seen[n] = true
	}
	return nil
}

func sectionByName(name string) (sectionDef, bool) {
	for _, d := range sectionDefs {
		if d.name == name {
			return d, true
		}
	}
	return sectionDef{}, false
}

// shown counts the pull requests a grouping displays.
func shown(groups []itemGroup, stale, held []Item) int {
	n := len(stale) + len(held)
	for _, g := range groups {
		n += len(g.items)
	}
	return n
}

// group splits items into the non-empty sections, in display order; the
// stale tail; and the stacked pull requests held until their bases are
// approved.
//
// Each item goes in the first section that matches, except that the folded
// sections, stacked and stale, are tried first wherever they are listed: they
// say a pull request isn't actionable today, which outranks whose turn it is.
// Stacked is tried before stale.
func group(team codeowners.Owner, items []Item, opts Options) ([]itemGroup, []Item, []Item) {
	names := opts.Sections
	if len(names) == 0 {
		names = DefaultSections
	}
	var defs []sectionDef
	for _, folded := range []string{SectionStacked, SectionStale} {
		if slices.Contains(names, folded) {
			d, _ := sectionByName(folded)
			defs = append(defs, d)
		}
	}
	for _, n := range names {
		if d, ok := sectionByName(n); ok && !d.folded {
			defs = append(defs, d)
		}
	}

	var stale, held []Item
	byName := map[string][]Item{}
	for _, it := range items {
		for _, d := range defs {
			if !d.match(it, opts) {
				continue
			}
			switch d.name {
			case SectionStacked:
				held = append(held, it)
			case SectionStale:
				stale = append(stale, it)
			default:
				byName[d.name] = append(byName[d.name], it)
			}
			break
		}
	}
	var groups []itemGroup
	for _, d := range defs {
		if list := byName[d.name]; len(list) > 0 {
			groups = append(groups, itemGroup{title: d.title(team), items: list})
		}
	}
	return groups, stale, held
}

// Blocks renders the queue as a Slack message payload: a header per section
// and a table of its pull requests. text is the fallback notifications show.
func Blocks(repo string, team codeowners.Owner, items []Item, opts Options) map[string]any {
	groups, stale, held := group(team, items, opts)
	title := fmt.Sprintf("Pull requests for %s in %s (%d)", team.String(), repo, shown(groups, stale, held))
	blocks := []any{
		map[string]any{"type": "header", "text": map[string]any{"type": "plain_text", "text": title}},
	}
	if len(groups)+len(stale)+len(held) == 0 {
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
	if len(held) > 0 {
		blocks = append(blocks, map[string]any{
			"type": "context",
			"elements": []any{map[string]any{"type": "mrkdwn",
				"text": fmt.Sprintf("%d waiting on their base pull requests: %s", len(held), heldList(held))}},
		})
	}
	return map[string]any{"text": title, "blocks": blocks}
}

// tableRows lays out a section's pull requests, header first.
//
// Slack won't be told how wide to make a column, and it gives them room
// evenly, so every column taken from the title's share shows. The number and
// title therefore share one cell, and why goes in brackets after who.
func tableRows(items []Item, opts Options) [][]any {
	header := []any{rawCell("Pull request"), rawCell("Jira"), rawCell("Waiting on"), rawCell("Idle")}
	if opts.ShowReasons {
		header = append(header, rawCell("Ours because"))
	}
	rows := [][]any{header}
	for _, it := range items {
		number := it.PR.Key[strings.LastIndex(it.PR.Key, "#"):]
		row := []any{
			prCell(it, number),
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

// prCell links the pull request's number, then its title and author as text,
// then the held pull requests it blocks, so whoever reads the row sees that
// reviewing it frees others.
func prCell(it Item, number string) map[string]any {
	elements := []any{
		map[string]any{"type": "link", "url": it.PR.URL, "text": number},
		map[string]any{"type": "text", "text": " " + titleWithAuthor(it.PR)},
	}
	if len(it.Blocks) > 0 {
		elements = append(elements, map[string]any{"type": "text", "text": " — blocks ", "style": map[string]any{"bold": true}})
		for i, pr := range it.Blocks {
			if i > 0 {
				elements = append(elements, map[string]any{"type": "text", "text": ", "})
			}
			elements = append(elements, map[string]any{
				"type": "link", "url": pr.URL, "text": pr.Key[strings.LastIndex(pr.Key, "#"):],
			})
		}
	}
	return map[string]any{
		"type":     "rich_text",
		"elements": []any{map[string]any{"type": "rich_text_section", "elements": elements}},
	}
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
	text := opts.idleText(since)
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
