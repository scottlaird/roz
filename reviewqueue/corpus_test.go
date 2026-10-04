package reviewqueue

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/scottlaird/roz/codeowners"
	"github.com/scottlaird/roz/github"
)

// The corner-case corpus: each case is a real pull request that a naive
// reading of GitHub's review requests got wrong, reduced to the state that
// decided it, with the answer the queue should give. A new misfiling joins
// as a new case.
//
// The world is shaped like the repository they came from, with the names
// changed: a catch-all rule naming five teams, narrower rules for storage/,
// platform/ and the shared common/sync/, and storage nested inside
// platform, so every storage member is a platform member too.

const corpusCodeowners = `
* @acme/server @acme/platform @acme/storage @acme/web @acme/saas
/platform/ @acme/platform
/storage/ @acme/storage
/common/sync/ @acme/platform @acme/storage
`

var (
	corpusTeam     = codeowners.NormalizeOwner("@acme/storage")
	storageMembers = []string{"alice", "bob", "carol"}
	platformOnly   = []string{"dave", "erin"}
	corpusT0       = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
)

func corpusConfig(t *testing.T) (map[string]*codeowners.File, Config) {
	t.Helper()
	f, err := codeowners.ParseString(corpusCodeowners)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]*codeowners.File{"main": f, "base-branch": f}
	return owners, Config{
		Team: corpusTeam, Members: storageMembers, MaxRuleOwners: 3,
		TeamMembers: map[string][]string{
			"acme/storage":  storageMembers,
			"acme/platform": slices.Concat(storageMembers, platformOnly),
			"acme/server":   {"sean"},
			"acme/web":      {"cleo"},
			"acme/saas":     {"sia"},
		},
	}
}

// corpusPR is a ready-for-review pull request by a platform member who isn't on
// storage, on main, touching storage's files; cases change what matters.
func corpusPR(number int, change func(*github.OpenPR)) github.OpenPR {
	p := github.OpenPR{
		Key: "acme/service#" + itoa(number), URL: "https://example.com/" + itoa(number),
		Title: "change", Author: "dave", BaseRef: "main", HeadRef: "head-" + itoa(number),
		CreatedAt: corpusT0, Files: []string{"storage/replica.go"},
		LastActor: "dave", LastActivity: corpusT0.Add(time.Hour),
	}
	change(&p)
	return p
}

func itoa(n int) string { return strconv.Itoa(n) }

// verdict is what the queue should say about the case's pull request.
type verdict struct {
	claimed   bool
	waitingOn []string // checked when claimed
	section   Section  // checked when claimed
	held      bool
}

func TestCorpus(t *testing.T) {
	base := github.PRBranch{Key: "acme/service#1", Number: 1, HeadRef: "base-branch", BaseRef: "main"}

	for _, tc := range []struct {
		name     string
		pr       github.OpenPR
		branches []github.PRBranch
		ignore   []string
		want     verdict
	}{
		{
			// the files that made GitHub request storage left the diff.
			// GitHub never withdraws a request.
			name: "stale request: storage's files left the diff",
			pr: corpusPR(6572, func(p *github.OpenPR) {
				p.Files = []string{"platform/server.go"}
				p.RequestedTeams = []string{"acme/storage", "acme/platform"}
			}),
			want: verdict{claimed: false},
		},
		{
			// storage requested only through the catch-all rule.
			name: "stale request: only the catch-all names storage",
			pr: corpusPR(6573, func(p *github.OpenPR) {
				p.Files = []string{"README.md"}
				p.RequestedTeams = []string{"acme/storage", "acme/server", "acme/saas"}
			}),
			want: verdict{claimed: false},
		},
		{
			// four teams requested. platform owns sync alongside storage,
			// and every storage member is in platform, so one storage approval
			// satisfies both; server and saas came from the catch-all.
			name: "nested teams: one storage approval covers platform",
			pr: corpusPR(8893, func(p *github.OpenPR) {
				p.Files = []string{"storage/replica.go", "common/sync/mutex.go"}
				p.RequestedTeams = []string{"acme/storage", "acme/platform", "acme/server", "acme/saas"}
			}),
			want: verdict{claimed: true, waitingOn: []string{"@acme/storage"}, section: NotYetReviewed},
		},
		{
			// a storage member commented and the team's
			// request is still open. The request says nothing about whose
			// turn it is; the member moved last, so it's the author's.
			name: "team request after engagement: the member moved last",
			pr: corpusPR(8733, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"acme/storage"}
				p.Commenters = []string{"carol"}
				p.LastActor = "carol"
			}),
			want: verdict{claimed: true, waitingOn: []string{"@dave"}, section: OnAuthor},
		},
		{
			name: "team request after engagement: the author moved last",
			pr: corpusPR(8734, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"acme/storage"}
				p.Commenters = []string{"carol"}
				p.LastActor = "dave"
			}),
			want: verdict{claimed: true, waitingOn: []string{"@carol"}, section: OnMember},
		},
		{
			// a storage member asked by name on files only platform
			// owns was asked as platform's reviewer, since storage is inside platform.
			name: "shared member on the parent team's files",
			pr: corpusPR(8917, func(p *github.OpenPR) {
				p.Files = []string{"platform/server.go"}
				p.RequestedUsers = []string{"carol"}
			}),
			want: verdict{claimed: false},
		},
		{
			// the reviewer commented, then was listed as requested
			// again. A comment doesn't clear a request; the reviewer moved
			// last, so it's the author's turn.
			name: "reviewer re-requested after commenting",
			pr: corpusPR(8969, func(p *github.OpenPR) {
				p.RequestedUsers = []string{"bob"}
				p.Commenters = []string{"bob"}
				p.LastActor = "bob"
			}),
			want: verdict{claimed: true, waitingOn: []string{"@dave"}, section: OnAuthor},
		},
		{
			// a storage author and a storage reviewer on sync, which
			// platform co-owns, stacked on an unapproved pull request. It's
			// storage's conversation, held until its base is approved.
			name: "member conversation on the parent's files, stacked",
			pr: corpusPR(8894, func(p *github.OpenPR) {
				p.Author = "carol"
				p.Files = []string{"common/sync/mutex.go"}
				p.BaseRef = "base-branch"
				p.Commenters = []string{"alice"}
				p.LastActor = "alice"
			}),
			branches: []github.PRBranch{base},
			want:     verdict{claimed: true, waitingOn: []string{"@carol"}, section: OnAuthor, held: true},
		},
		{
			// once the base is approved, the stacked
			// pull request is shown.
			name: "stacked on an approved base",
			pr: corpusPR(8711, func(p *github.OpenPR) {
				p.BaseRef = "base-branch"
				p.RequestedTeams = []string{"acme/storage"}
			}),
			branches: []github.PRBranch{{Key: base.Key, Number: 1, HeadRef: "base-branch", BaseRef: "main", ReviewDecision: "APPROVED"}},
			want:     verdict{claimed: true, waitingOn: []string{"@acme/storage"}, section: NotYetReviewed},
		},
		{
			// a retired ruleset's required team is still
			// requested. No CODEOWNERS rule names it, so it stays until
			// listed in ignore-team.
			name: "retired gate, not yet ignored",
			pr: corpusPR(8441, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"acme/storage", "acme/release-gate"}
			}),
			want: verdict{claimed: true, waitingOn: []string{"@acme/storage", "@acme/release-gate"}, section: NotYetReviewed},
		},
		{
			name: "retired gate, ignored",
			pr: corpusPR(8442, func(p *github.OpenPR) {
				p.RequestedTeams = []string{"acme/storage", "acme/release-gate"}
			}),
			ignore: []string{"@acme/release-gate"},
			want:   verdict{claimed: true, waitingOn: []string{"@acme/storage"}, section: NotYetReviewed},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owners, cfg := corpusConfig(t)
			cfg.Branches = append(tc.branches, github.PRBranch{Key: tc.pr.Key, HeadRef: tc.pr.HeadRef, BaseRef: tc.pr.BaseRef})
			for _, ig := range tc.ignore {
				cfg.IgnoreTeams = append(cfg.IgnoreTeams, codeowners.NormalizeOwner(ig))
			}
			items, _ := Select([]github.OpenPR{tc.pr}, owners, cfg)
			if !tc.want.claimed {
				if len(items) != 0 {
					t.Fatalf("claimed for storage (%v), but it isn't storage's", items[0].Reasons)
				}
				return
			}
			if len(items) != 1 {
				t.Fatalf("not claimed for storage")
			}
			it := items[0]
			if !slices.Equal(it.WaitingOn, tc.want.waitingOn) {
				t.Errorf("waiting on %v (%s), want %v", it.WaitingOn, it.Why, tc.want.waitingOn)
			}
			if it.Section != tc.want.section {
				t.Errorf("section %v, want %v", it.Section, tc.want.section)
			}
			if it.Held != tc.want.held {
				t.Errorf("held = %v, want %v", it.Held, tc.want.held)
			}
		})
	}
}
