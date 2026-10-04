package reviewqueue

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/scottlaird/roz/github"
)

var _ Source = (*github.Client)(nil)

// fakeSource serves one repository from memory and records what was asked.
type fakeSource struct {
	prs        []github.OpenPR
	codeowners map[string]string // base ref -> text
	members    map[string][]string

	codeownersReads []string
	teamsAsked      []string
}

func (f *fakeSource) OpenPullRequests(context.Context, string, func(int)) ([]github.OpenPR, error) {
	return f.prs, nil
}

func (f *fakeSource) Codeowners(_ context.Context, _, ref string) (string, string, error) {
	f.codeownersReads = append(f.codeownersReads, ref)
	text, ok := f.codeowners[ref]
	if !ok {
		return "", "", errors.New("no such ref")
	}
	return text, ".github/CODEOWNERS", nil
}

func (f *fakeSource) OpenPRBranches(context.Context, string) ([]github.PRBranch, error) {
	return nil, nil
}

func (f *fakeSource) TeamMembers(_ context.Context, teams []string) (map[string][]string, error) {
	f.teamsAsked = teams
	out := map[string][]string{}
	for _, t := range teams {
		out[t] = f.members[t]
	}
	return out, nil
}

func TestBuild(t *testing.T) {
	src := &fakeSource{
		prs: []github.OpenPR{
			pr(1, func(p *github.OpenPR) {
				p.Files = []string{"storage/engine.go"}
				p.RequestedTeams = []string{"org/storage"}
			}),
			pr(2, func(p *github.OpenPR) {
				p.Files = []string{"api/handler.go"}
				p.RequestedUsers = []string{"alice"}
			}),
			// A draft's base is never read.
			pr(3, func(p *github.OpenPR) {
				p.Draft = true
				p.BaseRef = "release"
			}),
		},
		codeowners: map[string]string{"main": testCodeowners},
		members:    map[string][]string{"org/storage": {"alice", "Bob"}},
	}

	cfg := Config{Team: team, MaxRuleOwners: 3}
	items, _, err := Build(context.Background(), src, "org/repo", cfg, nil)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if !slices.Equal(src.codeownersReads, []string{"main"}) {
		t.Errorf("read CODEOWNERS on %v, want main once and the draft's base never", src.codeownersReads)
	}
	for _, want := range []string{"org/storage", "org/core", "org/api", "org/platform", "org/infra"} {
		if !slices.Contains(src.teamsAsked, want) {
			t.Errorf("membership of %s was not read; asked for %v", want, src.teamsAsked)
		}
	}

	// Members came from the membership read: #2 is the team's only because
	// alice is asked for by name.
	var keys []string
	for _, it := range items {
		keys = append(keys, it.PR.Key)
	}
	if !slices.Contains(keys, src.prs[0].Key) || !slices.Contains(keys, src.prs[1].Key) {
		t.Errorf("queue is %v, want both #1 and #2", keys)
	}
}

func TestBuildNamesTheCodeownersThatFailed(t *testing.T) {
	src := &fakeSource{
		prs: []github.OpenPR{pr(1, func(p *github.OpenPR) { p.BaseRef = "gone" })},
	}
	_, _, err := Build(context.Background(), src, "org/repo", Config{Team: team}, nil)
	if err == nil || !strings.Contains(err.Error(), "org/repo CODEOWNERS on gone") {
		t.Errorf("error %v does not say which CODEOWNERS failed", err)
	}
}
