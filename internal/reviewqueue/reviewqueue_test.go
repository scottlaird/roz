package reviewqueue

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/roz/internal/codeowners"
	"github.com/scottlaird/roz/internal/github"
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
			name: "commented on, author hasn't answered",
			pr: pr(1, func(p *github.OpenPR) {
				p.Reviews = []github.Review{{Author: "erin", State: "COMMENTED", At: t0}}
				p.LastActor, p.LastActivity = "erin", t0
			}),
			want: "@carol", why: AuthorsTurn,
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
			got, why := waitingOn(tc.pr, team, tc.teamOwns)
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

func TestFormat(t *testing.T) {
	now := t0.Add(100 * 24 * time.Hour)
	item := func(n int, title string, sec Section, idle time.Duration) Item {
		return Item{
			PR: pr(n, func(p *github.OpenPR) {
				p.Title = title
				p.Key = fmt.Sprintf("org/repo#%d", 100+n)
				p.URL = fmt.Sprintf("https://example.com/%d", 100+n)
				p.LastActivity = now.Add(-idle)
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
			p.LastActivity = now.Add(-3 * time.Hour)
		}),
		Reasons: []string{"owns storage/engine.go"}, WaitingOn: []string{"@bob"},
		Why: "review requested", Section: OnMember, Jira: []string{"ABC-1", "ABC-2"},
	}, {
		PR: pr(2, func(p *github.OpenPR) {
			p.Key, p.URL, p.Title = "org/repo#102", "https://example.com/102", "no ticket"
			p.LastActivity = now.Add(-3 * time.Hour)
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
		`{"text":"#101 a change (@carol)","type":"link","url":"https://example.com/101"}`,
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
			p.LastActivity = now.Add(-50 * time.Hour)
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
		`{"text":"#101 a change (@carol)","type":"link","url":"https://example.com/101"}`,
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
