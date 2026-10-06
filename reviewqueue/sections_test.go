package reviewqueue

import (
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/roz/github"
)

func TestSelectMemberAuthored(t *testing.T) {
	// alice is a member; nothing about the diff is the team's.
	prs := []github.OpenPR{pr(1, func(p *github.OpenPR) {
		p.Author = "alice"
		p.Files = []string{"api/handler.go"}
		p.RequestedTeams = []string{"org/core"}
	})}

	if items, _ := Select(prs, mustOwners(t), config()); len(items) != 0 {
		t.Errorf("selected without IncludeMemberAuthored: %+v", items)
	}
	cfg := config()
	cfg.IncludeMemberAuthored = true
	items, _ := Select(prs, mustOwners(t), cfg)
	if len(items) != 1 || !items[0].ByMember || !items[0].AuthoredOnly {
		t.Fatalf("items = %+v, want one AuthoredOnly item", items)
	}
}

func TestSectionsChooseAndOrder(t *testing.T) {
	now := t0.Add(30 * 24 * time.Hour)
	item := func(n int, sec Section, byMember, authoredOnly bool, idle time.Duration) Item {
		return Item{
			PR: pr(n, func(p *github.OpenPR) {
				p.Title = "pr" + string(rune('0'+n))
				p.IdleSince = now.Add(-idle)
			}),
			WaitingOn: []string{"@x"}, Why: "review requested",
			Section: sec, ByMember: byMember, AuthoredOnly: authoredOnly,
		}
	}
	items := []Item{
		item(1, NotYetReviewed, false, false, time.Hour),
		item(2, NotYetReviewed, true, false, time.Hour),
		item(3, OnOthers, false, false, time.Hour),
		item(4, OnOthers, true, true, time.Hour),
		item(5, OnMember, false, false, 20*24*time.Hour),
	}
	opts := Options{Now: now, StaleAfter: 14 * 24 * time.Hour,
		Sections: []string{SectionMemberAuthored, SectionTeamUnreviewed, SectionStale}}

	groups, stale, held := group(team, items, opts)
	got := map[string][]string{}
	var order []string
	for _, g := range groups {
		order = append(order, g.title)
		for _, it := range g.items {
			got[g.title] = append(got[g.title], it.PR.Title)
		}
	}
	if strings.Join(order, " | ") != "Written by members of @org/storage | Not yet reviewed by @org/storage" {
		t.Errorf("section order = %v", order)
	}
	if strings.Join(got["Written by members of @org/storage"], ",") != "pr2,pr4" {
		t.Errorf("member_authored = %v; pr2 must go in the first section that matches", got)
	}
	if strings.Join(got["Not yet reviewed by @org/storage"], ",") != "pr1" {
		t.Errorf("team_unreviewed = %v", got)
	}
	if len(stale) != 1 || stale[0].PR.Title != "pr5" || len(held) != 0 {
		t.Errorf("stale = %v, held = %v", stale, held)
	}
	// pr3 is in no listed section, and the count says what is shown.
	if out := Format("org/repo", team, items, opts); !strings.Contains(out, "(4)") || strings.Contains(out, "pr3") {
		t.Errorf("Format with sections:\n%s", out)
	}
}

func TestAuthoredOnlyStaysOutOfTeamSections(t *testing.T) {
	it := Item{PR: pr(1, func(*github.OpenPR) {}), Section: OnOthers, ByMember: true, AuthoredOnly: true}
	groups, _, _ := group(team, []Item{it}, Options{Now: t0})
	if len(groups) != 0 {
		t.Errorf("a pull request the team only wrote appeared in %q", groups[0].title)
	}
}

func TestWithoutStaleItFallsIntoItsSection(t *testing.T) {
	now := t0.Add(30 * 24 * time.Hour)
	it := Item{PR: pr(1, func(p *github.OpenPR) { p.IdleSince = t0 }), Section: OnMember}
	groups, stale, _ := group(team, []Item{it}, Options{Now: now, StaleAfter: 24 * time.Hour,
		Sections: []string{SectionMemberWaiting}})
	if len(stale) != 0 || len(groups) != 1 {
		t.Errorf("groups = %v, stale = %v; without stale listed it belongs in its section", groups, stale)
	}
}

func TestValidateSections(t *testing.T) {
	if err := ValidateSections(DefaultSections); err != nil {
		t.Error(err)
	}
	for _, bad := range [][]string{{"announced"}, {"stale", "stale"}} {
		if err := ValidateSections(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if len(SectionNames()) != 7 {
		t.Errorf("SectionNames = %v", SectionNames())
	}
}
