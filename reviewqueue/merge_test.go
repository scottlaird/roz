package reviewqueue

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/roz/github"
)

func TestMerge(t *testing.T) {
	now := t0.Add(30 * 24 * time.Hour)
	item := func(repo string, n int, idle time.Duration) Item {
		return Item{
			PR: pr(n, func(p *github.OpenPR) {
				p.Key = fmt.Sprintf("org/%s#%d", repo, n)
				p.Title = fmt.Sprintf("%s-%d", repo, n)
				p.IdleSince = now.Add(-idle)
			}),
			WaitingOn: []string{"@x"}, Why: "review requested", Section: NotYetReviewed,
		}
	}
	queues := []Queue{
		{Repo: "org/big", Items: []Item{item("big", 1, 9*time.Hour), item("big", 2, 8*time.Hour), item("big", 3, 7*time.Hour)}},
		{Repo: "org/small", Items: []Item{item("small", 4, time.Hour)}},
		{Repo: "org/empty"},
		{Repo: "org/other", Items: []Item{item("other", 5, 2*time.Hour)}},
	}
	opts := Options{Now: now}

	if got := Merge(team, queues, opts, 0); len(got) != 4 {
		t.Errorf("threshold 0 merged: %+v", got)
	}

	got := Merge(team, queues, opts, 3)
	if len(got) != 2 || got[0].Repo != "org/big" || got[0].Merged {
		t.Fatalf("Merge = %+v, want org/big alone then one merged queue", got)
	}
	m := got[1]
	if !m.Merged || m.Repo != "small, other" || strings.Join(m.NothingIn, ",") != "empty" {
		t.Errorf("merged = %+v", m)
	}
	var titles []string
	for _, it := range m.Items {
		titles = append(titles, it.PR.Title)
	}
	if strings.Join(titles, ",") != "other-5,small-4" {
		t.Errorf("merged order = %v, want longest idle first across repositories", titles)
	}

	out := FormatQueue(m, team, opts)
	for _, want := range []string{"|other#5>", "|small#4>", "in small, other*", "Nothing waiting in empty"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatQueue missing %q:\n%s", want, out)
		}
	}
	if blocks := fmt.Sprint(BlocksQueue(m, team, opts)); !strings.Contains(blocks, "other#5") || !strings.Contains(blocks, "Nothing waiting in empty") {
		t.Errorf("BlocksQueue:\n%s", blocks)
	}
	if out := FormatQueue(got[0], team, opts); strings.Contains(out, "big#1") || !strings.Contains(out, "|#1>") {
		t.Errorf("an unmerged queue qualified its numbers:\n%s", out)
	}

	// One repository under the threshold has nothing to merge with.
	if got := Merge(team, queues[:2], opts, 3); len(got) != 2 || got[1].Merged {
		t.Errorf("single small repository merged: %+v", got)
	}

	// With nothing anywhere, the heading names them all.
	got = Merge(team, []Queue{{Repo: "org/a"}, {Repo: "org/b"}}, opts, 3)
	if len(got) != 1 || got[0].Repo != "a, b" || len(got[0].NothingIn) != 0 {
		t.Errorf("all empty = %+v", got)
	}
}
