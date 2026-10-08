package reviewqueue

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/roz/codeowners"
	"github.com/scottlaird/roz/github"
)

const testCodeowners = `
* @org/core @org/api @org/platform @org/infra
/storage/ @org/storage
/common/ @org/core @org/api @org/platform @org/infra @org/storage
`

var (
	t0   = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	team = codeowners.NormalizeOwner("@org/storage")
)

func mustOwners(t *testing.T) map[string]*codeowners.File {
	t.Helper()
	f, err := codeowners.ParseString(testCodeowners)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]*codeowners.File{"main": f}
}

func config() Config {
	return Config{Team: team, Members: []string{"alice", "Bob"}, MaxRuleOwners: 3}
}

func pr(n int, mutate func(*github.OpenPR)) github.OpenPR {
	p := github.OpenPR{
		Key:          "org/repo#" + string(rune('0'+n)),
		URL:          "https://example.com/" + string(rune('0'+n)),
		Title:        "change",
		Author:       "carol",
		BaseRef:      "main",
		CreatedAt:    t0,
		LastActor:    "carol",
		LastActivity: t0.Add(time.Duration(n) * time.Hour),
		IdleSince:    t0.Add(time.Duration(n) * time.Hour),
	}
	mutate(&p)
	return p
}

func TestSelect(t *testing.T) {
	prs := []github.OpenPR{
		// The team owns a file in the diff.
		pr(1, func(p *github.OpenPR) {
			p.Files = []string{"docs/readme.md", "storage/engine.go", "storage/log.go"}
			p.RequestedTeams = []string{"org/storage"}
		}),
		// Requested, but the files that triggered it have left the diff.
		pr(2, func(p *github.OpenPR) {
			p.Files = []string{"api/handler.go"}
			p.RequestedTeams = []string{"org/storage", "org/core"}
		}),
		// Requested only through a rule naming five owners.
		pr(3, func(p *github.OpenPR) {
			p.Files = []string{"common/util.go"}
			p.RequestedTeams = []string{"org/storage"}
		}),
		// A member asked for by name, on files the team doesn't own.
		pr(4, func(p *github.OpenPR) {
			p.Files = []string{"api/handler.go"}
			p.RequestedUsers = []string{"alice"}
		}),
		// A member assigning their own pull request.
		pr(5, func(p *github.OpenPR) {
			p.Author = "bob"
			p.Files = []string{"api/handler.go"}
			p.Assignees = []string{"bob"}
		}),
		// Drafts never wait on anyone.
		pr(6, func(p *github.OpenPR) {
			p.Draft = true
			p.Files = []string{"storage/engine.go"}
		}),
		// An approved pull request is its author's to merge.
		pr(7, func(p *github.OpenPR) {
			p.ReviewDecision = "APPROVED"
			p.Files = []string{"storage/engine.go"}
		}),
	}

	items, skipped := Select(prs, mustOwners(t), config())

	if len(items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(items), items)
	}
	if items[0].PR.Key != prs[0].Key || items[1].PR.Key != prs[3].Key {
		t.Fatalf("got %s, %s; want %s, %s", items[0].PR.Key, items[1].PR.Key, prs[0].Key, prs[3].Key)
	}
	if got := items[0].Reasons[0]; got != "owns storage/engine.go (+1 more)" {
		t.Errorf("reason = %q", got)
	}
	if got := items[1].Reasons[0]; got != "review requested from @alice" {
		t.Errorf("reason = %q", got)
	}

	if len(skipped) != 2 {
		t.Fatalf("got %d skipped, want 2: %+v", len(skipped), skipped)
	}
	if !strings.Contains(skipped[0].Reason, "nothing in the current diff") {
		t.Errorf("stale request skipped as %q", skipped[0].Reason)
	}
	if !strings.Contains(skipped[1].Reason, "5-owner rule") {
		t.Errorf("broad rule skipped as %q", skipped[1].Reason)
	}
}

// A member asked for by name on files another team owns, who is in that team too, was asked as its
// reviewer: the pull request isn't this team's. One who isn't in an owning team still counts, and
// a catch-all rule naming a team doesn't make its members owners.
func TestSelectMemberAskedAsAnotherOwner(t *testing.T) {
	file, err := codeowners.ParseString(testCodeowners + "/core/ @org/core\n")
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]*codeowners.File{"main": file}
	cfg := config()
	cfg.TeamMembers = map[string][]string{
		"org/core": {"Alice"},
		"org/api":  {"bob"},
	}
	prs := []github.OpenPR{
		// alice is in core, which owns the file.
		pr(1, func(p *github.OpenPR) {
			p.Files = []string{"core/engine.go"}
			p.RequestedUsers = []string{"alice"}
		}),
		// Bob isn't in core.
		pr(2, func(p *github.OpenPR) {
			p.Files = []string{"core/engine.go"}
			p.RequestedUsers = []string{"Bob"}
		}),
		// Bob is in api, but api is only named by the catch-all.
		pr(3, func(p *github.OpenPR) {
			p.Files = []string{"api/handler.go"}
			p.RequestedUsers = []string{"Bob"}
		}),
	}

	items, _ := Select(prs, owners, cfg)
	var got []string
	for _, it := range items {
		got = append(got, it.PR.Key)
	}
	want := []string{prs[1].Key, prs[2].Key}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A member's review or comment makes a pull request the team's when nothing else does, unless
// the member was engaging as another owning team's reviewer; a member author makes it the team's
// conversation regardless. Nested teams: alice is in core, which owns core/.
func TestSelectEngagement(t *testing.T) {
	file, err := codeowners.ParseString(testCodeowners + "/core/ @org/core\n")
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]*codeowners.File{"main": file}
	cfg := config()
	cfg.TeamMembers = map[string][]string{"org/core": {"alice"}}
	prs := []github.OpenPR{
		// A member reviewed files no team of theirs owns.
		pr(1, func(p *github.OpenPR) {
			p.Files = []string{"api/handler.go"}
			p.Reviews = []github.Review{{Author: "Bob", State: "COMMENTED", At: t0}}
		}),
		// alice reviewed as core, for an outside author.
		pr(2, func(p *github.OpenPR) {
			p.Files = []string{"core/engine.go"}
			p.Reviews = []github.Review{{Author: "alice", State: "COMMENTED", At: t0}}
		}),
		// The same, but the author is a member: a team conversation.
		pr(3, func(p *github.OpenPR) {
			p.Author = "bob"
			p.Files = []string{"core/engine.go"}
			p.Reviews = []github.Review{{Author: "alice", State: "COMMENTED", At: t0}}
			p.LastActor = "alice"
		}),
		// Only outsiders engaged.
		pr(4, func(p *github.OpenPR) {
			p.Files = []string{"api/handler.go"}
			p.Commenters = []string{"erin"}
		}),
	}

	items, _ := Select(prs, owners, cfg)
	var got []string
	for _, it := range items {
		got = append(got, it.PR.Key)
	}
	if want := []string{prs[0].Key, prs[2].Key}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, it := range items {
		if it.PR.Key == prs[2].Key {
			if strings.Join(it.WaitingOn, ", ") != "@bob" || it.Section != OnAuthor {
				t.Errorf("member conversation: waiting on %v in section %v, want @bob, OnAuthor", it.WaitingOn, it.Section)
			}
		}
	}
}

func TestSelectWithoutRuleLimit(t *testing.T) {
	cfg := config()
	cfg.MaxRuleOwners = 0
	items, _ := Select([]github.OpenPR{pr(1, func(p *github.OpenPR) {
		p.Files = []string{"common/util.go"}
	})}, mustOwners(t), cfg)
	if len(items) != 1 {
		t.Fatalf("with no limit a broad rule counts; got %d items", len(items))
	}
}

func TestWaitingOn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pr       github.OpenPR
		teamOwns bool
		ignore   []codeowners.Owner
		want     string
		why      string
	}{
		{
			name: "changes requested",
			pr: pr(1, func(p *github.OpenPR) {
				// alice's latest review is a comment, but her change request stands.
				p.Reviews = []github.Review{{Author: "alice", State: "COMMENTED"}}
				p.ChangesRequestedBy = []string{"alice"}
				p.RequestedUsers = []string{"bob"}
			}),
			want: "@carol", why: "changes requested by @alice",
		},
		{
			name: "unanswered review comments, whoever else is requested",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedUsers = []string{"bob", "erin"}
				p.Reviews = []github.Review{{Author: "bob", State: "COMMENTED"}}
				p.LastActor = "carol"
				p.UnansweredThreads = 1
			}),
			want: "@carol", why: UnansweredComments,
		},
		{
			name: "reviewers, dropping the team's stale request",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedUsers = []string{"bob"}
				p.RequestedTeams = []string{"org/storage", "org/core"}
			}),
			want: "@bob, @org/core", why: "review requested",
		},
		{
			name: "the team's request kept when it owns something",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"org/storage"}
			}),
			teamOwns: true,
			want:     "@org/storage", why: "review requested",
		},
		{
			name: "a requested reviewer who moved last is waiting on the author",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedUsers = []string{"Bob"}
				p.Commenters = []string{"bob"}
				p.LastActor, p.LastActivity = "bob", t0.Add(time.Hour)
			}),
			want: "@carol", why: AuthorsTurn,
		},
		{
			name: "other requested reviewers still count",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedUsers = []string{"bob", "erin"}
				p.Commenters = []string{"bob"}
				p.LastActor, p.LastActivity = "bob", t0.Add(time.Hour)
			}),
			want: "@erin", why: "review requested",
		},
		{
			name: "nobody asked and nobody reviewed",
			pr: pr(1, func(p *github.OpenPR) {
				p.LastActor = "carol"
			}),
			want: "@carol", why: NoReviewer,
		},
		{
			name: "commented on, author since updated",
			pr: pr(1, func(p *github.OpenPR) {
				p.Reviews = []github.Review{{Author: "erin", State: "COMMENTED", At: t0}}
				p.LastActor, p.LastActivity = "carol", t0.Add(time.Hour)
			}),
			want: "@erin", why: UpdatedSinceReview,
		},
		{
			name: "author since updated: an approver isn't waited on",
			pr: pr(1, func(p *github.OpenPR) {
				p.Reviews = []github.Review{
					{Author: "erin", State: "COMMENTED", At: t0},
					{Author: "bob", State: "APPROVED", At: t0},
				}
				p.Approvers = []string{"bob"}
				p.LastActor, p.LastActivity = "carol", t0.Add(time.Hour)
			}),
			want: "@erin", why: UpdatedSinceReview,
		},
		{
			name: "only approvers, nobody asked: it needs a reviewer",
			pr: pr(1, func(p *github.OpenPR) {
				p.Reviews = []github.Review{{Author: "bob", State: "APPROVED", At: t0}}
				p.Approvers = []string{"bob"}
				p.LastActor, p.LastActivity = "carol", t0.Add(time.Hour)
			}),
			want: "@carol", why: NoReviewer,
		},
		{
			name: "commented on, author hasn't answered",
			pr: pr(1, func(p *github.OpenPR) {
				p.Reviews = []github.Review{{Author: "erin", State: "COMMENTED", At: t0}}
				p.LastActor, p.LastActivity = "erin", t0
			}),
			want: "@carol", why: AuthorsTurn,
		},
		{
			name: "only the team's request left, a member commented, author hasn't answered",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"org/storage"}
				p.Reviews = []github.Review{{Author: "bob", State: "COMMENTED", At: t0}}
				p.LastActor, p.LastActivity = "bob", t0
			}),
			teamOwns: true,
			want:     "@carol", why: AuthorsTurn,
		},
		{
			name: "only the team's request left, a member commented, author since updated",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"org/storage"}
				p.Reviews = []github.Review{{Author: "bob", State: "COMMENTED", At: t0}}
				p.LastActor, p.LastActivity = "carol", t0.Add(time.Hour)
			}),
			teamOwns: true,
			want:     "@bob", why: UpdatedSinceReview,
		},
		{
			name: "a member commented and is still requested, author since updated",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"org/storage"}
				p.RequestedUsers = []string{"Bob"}
				p.Reviews = []github.Review{{Author: "bob", State: "COMMENTED", At: t0}}
				p.LastActor, p.LastActivity = "carol", t0.Add(time.Hour)
			}),
			teamOwns: true,
			want:     "@bob", why: UpdatedSinceReview,
		},
		{
			name: "only the team's request left, and only an outsider commented",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"org/storage"}
				p.Reviews = []github.Review{{Author: "erin", State: "COMMENTED", At: t0}}
				p.LastActor, p.LastActivity = "erin", t0
			}),
			teamOwns: true,
			want:     "@org/storage", why: "review requested",
		},
		{
			name: "a member commented, and another team still owes a review",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"org/storage", "org/gate"}
				p.Reviews = []github.Review{{Author: "bob", State: "COMMENTED", At: t0}}
				p.LastActor, p.LastActivity = "bob", t0
			}),
			teamOwns: true,
			want:     "@carol, @org/gate", why: AuthorsTurn,
		},
		{
			name: "an ignored team is never waited on",
			pr: pr(1, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"org/storage", "org/retired-gate"}
			}),
			teamOwns: true,
			ignore:   []codeowners.Owner{"org/retired-gate"},
			want:     "@org/storage", why: "review requested",
		},
		{
			name: "the author's own comments are not a review",
			pr: pr(1, func(p *github.OpenPR) {
				p.Reviews = []github.Review{{Author: "carol", State: "COMMENTED", At: t0}}
			}),
			want: "@carol", why: NoReviewer,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config()
			cfg.IgnoreTeams = tc.ignore
			got, why := waitingOn(tc.pr, team, tc.teamOwns, false, nil, nil, cfg)
			if strings.Join(got, ", ") != tc.want || why != tc.why {
				t.Errorf("got %v (%s), want %s (%s)", got, why, tc.want, tc.why)
			}
		})
	}
}

func TestSections(t *testing.T) {
	members := map[string]bool{"alice": true, "bob": true}
	// bob, a member, has commented; the rest start from there.
	engagedPR := func(mutate func(*github.OpenPR)) github.OpenPR {
		return pr(1, func(p *github.OpenPR) {
			p.Commenters = []string{"bob"}
			if mutate != nil {
				mutate(p)
			}
		})
	}
	for _, tc := range []struct {
		name      string
		pr        github.OpenPR
		waitingOn []string
		why       string
		want      Section
	}{
		{
			name:      "no member has responded, even with one requested",
			pr:        pr(1, func(p *github.OpenPR) { p.Commenters = []string{"outsider"} }),
			waitingOn: []string{"@alice"}, want: NotYetReviewed,
		},
		{
			name: "the author commenting on their own pull request isn't a response",
			pr: pr(1, func(p *github.OpenPR) {
				p.Author = "alice"
				p.Commenters = []string{"alice"}
			}),
			waitingOn: []string{"@bob"}, want: NotYetReviewed,
		},
		{name: "a member", pr: engagedPR(nil), waitingOn: []string{"@core", "@Alice"}, want: OnMember},
		{name: "the team as a whole again", pr: engagedPR(nil), waitingOn: []string{"@org/storage"}, want: NotYetReviewed},
		{name: "someone else", pr: engagedPR(nil), waitingOn: []string{"@org/core"}, want: OnOthers},
		{name: "reviewed, author's turn", pr: engagedPR(nil), waitingOn: []string{"@carol"}, why: AuthorsTurn, want: OnAuthor},
		{name: "updated for a member to re-review", pr: engagedPR(nil), waitingOn: []string{"@bob"}, why: UpdatedSinceReview, want: OnMember},
		{
			name: "a change request",
			pr: engagedPR(func(p *github.OpenPR) {
				p.ChangesRequestedBy = []string{"alice"}
			}),
			waitingOn: []string{"@carol"}, want: OnAuthor,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := section(tc.pr, tc.waitingOn, tc.why, team, members); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJiraKeys(t *testing.T) {
	p := pr(1, func(p *github.OpenPR) {
		p.Title = "[CDSS-1172] fix UTF-8 handling, see ABC-9"
		p.Body = "Fixes: https://example.atlassian.net/browse/CDSS-1919\nAlso CDSS-1172 and SHA-256.\nhttps://issues.apache.org/jira/browse/CASSANDRA-123"
	})
	for _, tc := range []struct {
		name     string
		prefixes []string
		want     []string
	}{
		{name: "any project but the standards", want: []string{"CDSS-1172", "ABC-9", "CDSS-1919", "CASSANDRA-123"}},
		{name: "configured projects only", prefixes: []string{"cdss"}, want: []string{"CDSS-1172", "CDSS-1919"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := JiraKeys(p, tc.prefixes); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSLOMarks(t *testing.T) {
	opts := Options{Now: t0, SLOWarn: 48 * time.Hour, SLOBreach: 7 * 24 * time.Hour}
	for _, tc := range []struct {
		idle time.Duration
		want string
	}{
		{47 * time.Hour, "47h"},
		{49 * time.Hour, "🟡 2d"},
		{7 * 24 * time.Hour, "🟡 7d"},
		{8 * 24 * time.Hour, "🔴 8d"},
	} {
		if got := opts.idleText(t0.Add(-tc.idle)); got != tc.want {
			t.Errorf("idle %v: got %q, want %q", tc.idle, got, tc.want)
		}
	}
	if got := (Options{Now: t0}).idleText(t0.Add(-30 * 24 * time.Hour)); got != "30d" {
		t.Errorf("with no thresholds: got %q", got)
	}
}

func TestFormat(t *testing.T) {
	now := t0.Add(100 * 24 * time.Hour)
	item := func(n int, title string, sec Section, idle time.Duration) Item {
		return Item{
			PR: pr(n, func(p *github.OpenPR) {
				p.Title = title
				p.Key = fmt.Sprintf("org/repo#%d", 100+n)
				p.URL = fmt.Sprintf("https://example.com/%d", 100+n)
				p.IdleSince = now.Add(-idle)
			}),
			Reasons:   []string{"owns storage/engine.go"},
			WaitingOn: []string{"@bob"},
			Why:       "review requested",
			Section:   sec,
			Jira:      []string{"ABC-1"},
		}
	}
	items := []Item{
		item(1, "fix <thing> & more", NotYetReviewed, 73*time.Hour),
		item(2, "a member's turn", OnMember, 5*time.Hour),
		item(3, "author's turn", OnAuthor, 2*time.Hour),
		item(4, "ancient", OnOthers, 90*24*time.Hour),
	}
	opts := Options{Now: now, StaleAfter: 60 * 24 * time.Hour, JiraURL: "https://example.atlassian.net/browse/"}
	got := Format("org/repo", team, items, opts)
	for _, want := range []string{
		"*Pull requests for @org/storage in org/repo* (4)",
		"*Not yet reviewed by @org/storage* (1)",
		"<https://example.com/101|#101> fix &lt;thing&gt; &amp; more (@carol) <https://example.atlassian.net/browse/ABC-1|ABC-1>",
		"waiting on @bob (review requested) · idle 3d\n",
		"*Waiting on a member of @org/storage* (1)",
		"*Waiting on the author* (1)",
		"_1 idle over 60d:_ <https://example.com/104|#104>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "Not yet reviewed") > strings.Index(got, "Waiting on a member") {
		t.Errorf("pull requests needing a reviewer should come first:\n%s", got)
	}
	if strings.Contains(got, "Waiting on someone else") || strings.Contains(got, "owns ") {
		t.Errorf("the stale item's section and the reasons should both be absent:\n%s", got)
	}

	opts.ShowReasons = true
	if !strings.Contains(Format("org/repo", team, items, opts), "_owns storage/engine.go_") {
		t.Error("ShowReasons should add the reasons")
	}
	if !strings.Contains(Format("org/repo", team, nil, Options{Now: now}), "Nothing waiting.") {
		t.Error("an empty queue should say so")
	}
}

func TestBlocks(t *testing.T) {
	now := t0.Add(100 * 24 * time.Hour)
	items := []Item{{
		PR: pr(1, func(p *github.OpenPR) {
			p.Key, p.URL, p.Title = "org/repo#101", "https://example.com/101", "a change"
			p.IdleSince = now.Add(-3 * time.Hour)
		}),
		Reasons: []string{"owns storage/engine.go"}, WaitingOn: []string{"@bob"},
		Why: "review requested", Section: OnMember, Jira: []string{"ABC-1", "ABC-2"},
	}, {
		PR: pr(2, func(p *github.OpenPR) {
			p.Key, p.URL, p.Title = "org/repo#102", "https://example.com/102", "no ticket"
			p.IdleSince = now.Add(-3 * time.Hour)
		}),
		WaitingOn: []string{"@carol"}, Why: NoReviewer, Section: NotYetReviewed,
	}}
	render := func(opts Options) string {
		body, err := json.Marshal(Blocks("org/repo", team, items, opts))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	got := render(Options{Now: now, JiraURL: "https://example.atlassian.net/browse"})
	for _, want := range []string{
		`"text":"Pull requests for @org/storage in org/repo (2)"`,
		`"type":"table"`,
		`{"text":"#101","type":"link","url":"https://example.com/101"},{"text":" a change (@carol)","type":"text"}`,
		`{"text":"@bob (review requested)","type":"raw_text"}`,
		`{"text":"ABC-1","type":"link","url":"https://example.atlassian.net/browse/ABC-1"},{"text":", ","type":"text"},{"text":"ABC-2"`,
		`{"text":"–","type":"raw_text"}`,
		`{"text":"3h","type":"raw_text"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Index(got, "Not yet reviewed") > strings.Index(got, "Waiting on a member") {
		t.Errorf("pull requests needing a reviewer should come first: %s", got)
	}
	if strings.Contains(got, "null") || strings.Contains(got, "Ours because") {
		t.Errorf("no nulls, and no reasons unless asked for: %s", got)
	}
	if !strings.Contains(render(Options{Now: now, ShowReasons: true}), "Ours because") {
		t.Error("ShowReasons should add the reasons column")
	}
	if !strings.Contains(render(Options{Now: now}), `{"text":"ABC-1, ABC-2","type":"raw_text"}`) {
		t.Error("without a Jira base, keys should be plain text")
	}
}

func TestDataTables(t *testing.T) {
	now := t0.Add(100 * 24 * time.Hour)
	items := []Item{{
		PR: pr(1, func(p *github.OpenPR) {
			p.Key, p.URL, p.Title = "org/repo#101", "https://example.com/101", "a change"
			p.IdleSince = now.Add(-50 * time.Hour)
		}),
		WaitingOn: []string{"@bob"}, Why: "review requested", Section: OnMember,
	}}
	body, err := json.Marshal(Blocks("org/repo", team, items, Options{Now: now, DataTables: true, PageSize: 10}))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		`"type":"data_table"`,
		`"caption":"Waiting on a member of @org/storage (1)"`,
		`"page_size":10`,
		`{"text":"2d","type":"raw_number","value":50}`,
		`{"text":"Pull request","type":"raw_text"},{"text":"Jira","type":"raw_text"},{"text":"Waiting on","type":"raw_text"},{"text":"Idle","type":"raw_text"}`,
		`{"text":"#101","type":"link","url":"https://example.com/101"},{"text":" a change (@carol)","type":"text"}`,
		`{"text":"@bob (review requested)","type":"raw_text"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "column_settings") || strings.Contains(got, `"type":"section"`) {
		t.Errorf("a data table needs no column settings or separate heading: %s", got)
	}
}

func TestStacks(t *testing.T) {
	branch := func(n int64, head, base, decision string) github.PRBranch {
		return github.PRBranch{
			Key: fmt.Sprintf("org/repo#%d", n), URL: fmt.Sprintf("https://example.com/%d", n),
			Number: n, HeadRef: head, BaseRef: base, ReviewDecision: decision,
		}
	}
	cfg := config()
	cfg.Branches = []github.PRBranch{
		branch(10, "a", "main", "APPROVED"),
		branch(11, "b", "a", "REVIEW_REQUIRED"),
		branch(12, "c", "b", "REVIEW_REQUIRED"),
		branch(20, "x", "main", "APPROVED"),
		branch(21, "y", "x", "REVIEW_REQUIRED"),
		// A cycle GitHub wouldn't allow, which must still not loop.
		branch(30, "p", "q", ""),
		branch(31, "q", "p", ""),
	}
	stacked := func(n int, head, base string) github.OpenPR {
		return pr(n, func(p *github.OpenPR) {
			p.HeadRef, p.BaseRef = head, base
			p.Files = []string{"storage/engine.go"}
		})
	}
	owners := mustOwners(t)
	for _, base := range []string{"b", "x", "release", "q"} {
		owners[base] = owners["main"]
	}
	items, _ := Select([]github.OpenPR{
		stacked(1, "c", "b"),       // on #11 (unapproved) on #10 (approved): held
		stacked(2, "y", "x"),       // on #20, approved: shown
		stacked(3, "r", "release"), // on a branch no pull request owns: shown
		stacked(4, "p", "q"),       // in a cycle: held, and terminates
	}, owners, cfg)

	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.PR.Key] = it
	}
	check := func(key string, held bool, chain ...int64) {
		t.Helper()
		it := byKey[key]
		if it.Held != held {
			t.Errorf("%s held = %v, want %v", key, it.Held, held)
		}
		var got []int64
		for _, b := range it.StackedOn {
			got = append(got, b.Number)
		}
		if fmt.Sprint(got) != fmt.Sprint(chain) {
			t.Errorf("%s stacked on %v, want %v", key, got, chain)
		}
	}
	check(pr(1, func(*github.OpenPR) {}).Key, true, 11, 10)
	check(pr(2, func(*github.OpenPR) {}).Key, false, 20)
	check(pr(3, func(*github.OpenPR) {}).Key, false)
	check(pr(4, func(*github.OpenPR) {}).Key, true, 31)

	// #11 isn't in the queue, so #1 only shows in the held line; nothing is
	// marked as blocking it.
	for _, it := range items {
		if len(it.Blocks) > 0 {
			t.Errorf("%s blocks %v, but none of its dependants' bases are in the queue", it.PR.Key, it.Blocks)
		}
	}

	now := t0.Add(24 * time.Hour)
	got := Format("org/repo", team, items, Options{Now: now})
	if !strings.Contains(got, "_2 waiting on their base pull requests:_") ||
		!strings.Contains(got, "(on <https://example.com/11|#11>)") {
		t.Errorf("held pull requests should be listed with their nearest unapproved base:\n%s", got)
	}
	if strings.Count(got, "https://example.com/1|") != 1 {
		t.Errorf("a held pull request belongs only in the held line:\n%s", got)
	}
}

func TestBlockers(t *testing.T) {
	cfg := config()
	cfg.Branches = []github.PRBranch{
		{Key: pr(1, func(*github.OpenPR) {}).Key, Number: 1, HeadRef: "a", BaseRef: "main"},
		{Key: pr(2, func(*github.OpenPR) {}).Key, Number: 2, HeadRef: "b", BaseRef: "a"},
		{Key: pr(3, func(*github.OpenPR) {}).Key, Number: 3, HeadRef: "c", BaseRef: "b"},
	}
	stacked := func(n int, head, base string) github.OpenPR {
		return pr(n, func(p *github.OpenPR) {
			p.HeadRef, p.BaseRef = head, base
			p.URL = fmt.Sprintf("https://example.com/%d", n)
			p.Files = []string{"storage/engine.go"}
		})
	}
	owners := mustOwners(t)
	owners["a"], owners["b"] = owners["main"], owners["main"]
	items, _ := Select([]github.OpenPR{stacked(1, "a", "main"), stacked(2, "b", "a"), stacked(3, "c", "b")}, owners, cfg)

	var base Item
	for _, it := range items {
		if it.PR.Key == pr(1, func(*github.OpenPR) {}).Key {
			base = it
		}
	}
	if base.Held || len(base.Blocks) != 2 {
		t.Fatalf("the base of a, b, c should be shown and block both: held=%v blocks=%v", base.Held, base.Blocks)
	}

	opts := Options{Now: t0.Add(24 * time.Hour)}
	text := Format("org/repo", team, items, opts)
	if !strings.Contains(text, "*blocks <https://example.com/2|#2>, <https://example.com/3|#3>*") {
		t.Errorf("the base's row should say what it blocks:\n%s", text)
	}
	body, _ := json.Marshal(Blocks("org/repo", team, items, opts))
	if !strings.Contains(string(body), `{"style":{"bold":true},"text":" — blocks ","type":"text"},{"text":"#2","type":"link","url":"https://example.com/2"}`) {
		t.Errorf("the table's pull request cell should list what it blocks: %s", body)
	}
}

// GitHub requests every owning team, but one approval can satisfy several.
// Waiting-on shows only what CODEOWNERS still needs.
func TestWaitingOnFewestApprovals(t *testing.T) {
	cfg := config()
	cfg.TeamMembers = map[string][]string{
		"org/storage": {"alice", "bob"},
		"org/core":    {"alice", "bob", "carol"},
		"org/api":     {"dave"},
	}
	// Two storage files, and two files any of core, api or platform may
	// approve. Every storage member is in core, so storage covers all four.
	files := []string{"storage/a.go", "storage/b.go", "cmd/x.go", "cmd/y.go"}
	base := func(mutate func(*github.OpenPR)) github.OpenPR {
		return pr(1, func(p *github.OpenPR) {
			p.Files = files
			p.RequestedTeams = []string{"org/storage", "org/core", "org/api", "org/platform"}
			if mutate != nil {
				mutate(p)
			}
		})
	}
	f, err := codeowners.ParseString("* @org/core @org/api @org/platform\n/storage/ @org/storage\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		pr   github.OpenPR
		want string
	}{
		{name: "one nested team covers everything", pr: base(nil), want: "@org/storage"},
		{
			name: "a member asked by name stands for their team",
			pr:   base(func(p *github.OpenPR) { p.RequestedUsers = []string{"Alice"} }),
			want: "@Alice",
		},
		{
			name: "a team no rule names was asked for another reason, and stays",
			pr:   base(func(p *github.OpenPR) { p.RequestedTeams = append(p.RequestedTeams, "org/deploy-gate") }),
			want: "@org/storage, @org/deploy-gate",
		},
		{
			name: "a core approval leaves only storage's files",
			pr:   base(func(p *github.OpenPR) { p.Approvers = []string{"carol"} }),
			want: "@org/storage",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			needed := neededOwners(tc.pr, f, cfg)
			got, why := waitingOn(tc.pr, team, true, false, needed, namedTeams(f), cfg)
			if strings.Join(got, ", ") != tc.want || why != "review requested" {
				t.Errorf("got %v (%s), want %s", got, why, tc.want)
			}
		})
	}
}
