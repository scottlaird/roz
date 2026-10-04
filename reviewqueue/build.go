package reviewqueue

import (
	"context"
	"fmt"
	"sort"

	"github.com/scottlaird/roz/codeowners"
	"github.com/scottlaird/roz/github"
)

// Source is what Build reads from GitHub. *github.Client satisfies it; a
// caller holding its own copy of a repository's state can satisfy it from
// that instead.
type Source interface {
	OpenPullRequests(ctx context.Context, repo string, progress func(read int)) ([]github.OpenPR, error)
	Codeowners(ctx context.Context, repo, ref string) (text, path string, err error)
	OpenPRBranches(ctx context.Context, repo string) ([]github.PRBranch, error)
	TeamMembers(ctx context.Context, teams []string) (map[string][]string, error)
}

// Build reads one repository's open pull requests and selects cfg.Team's
// queue from them.
//
// cfg needs only the caller's choices: Team, and optionally MaxRuleOwners,
// JiraPrefixes and IgnoreTeams. Build fills in Members, TeamMembers and
// Branches from src. logf, which may be nil, hears about each read.
func Build(ctx context.Context, src Source, repo string, cfg Config, logf func(format string, args ...any)) ([]Item, []Skipped, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	logf("%s: listing open pull requests", repo)
	prs, err := src.OpenPullRequests(ctx, repo, func(read int) {
		logf("%s: %d read", repo, read)
	})
	if err != nil {
		return nil, nil, err
	}

	// One CODEOWNERS per base branch: a stack's upper pull requests are
	// judged by the file on the branch they would merge into. Drafts are left
	// out of the queue, so their bases need not be read.
	owners := map[string]*codeowners.File{}
	for _, pr := range prs {
		if _, seen := owners[pr.BaseRef]; seen || pr.Draft {
			continue
		}
		logf("%s: reading CODEOWNERS on %s", repo, pr.BaseRef)
		text, _, err := src.Codeowners(ctx, repo, pr.BaseRef)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s CODEOWNERS on %s: %w", repo, pr.BaseRef, err)
		}
		file, err := codeowners.ParseString(text)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing %s CODEOWNERS on %s: %w", repo, pr.BaseRef, err)
		}
		owners[pr.BaseRef] = file
	}

	logf("%s: listing open pull request branches, for stacks", repo)
	branches, err := src.OpenPRBranches(ctx, repo)
	if err != nil {
		return nil, nil, err
	}

	// Membership of every team the CODEOWNERS files name, so the report can
	// tell when one approval satisfies several teams.
	teamSet := map[string]bool{string(cfg.Team): true}
	for _, file := range owners {
		for _, t := range file.Teams() {
			teamSet[string(t)] = true
		}
	}
	refs := make([]string, 0, len(teamSet))
	for t := range teamSet {
		refs = append(refs, t)
	}
	sort.Strings(refs)
	logf("%s: reading membership of %d teams", repo, len(refs))
	teamMembers, err := src.TeamMembers(ctx, refs)
	if err != nil {
		return nil, nil, fmt.Errorf("reading team membership: %w", err)
	}

	cfg.Branches = branches
	cfg.TeamMembers = teamMembers
	if cfg.Members == nil {
		cfg.Members = teamMembers[string(cfg.Team)]
	}

	items, skipped := Select(prs, owners, cfg)
	logf("%s: %d for %s, %d requested but not its", repo, len(items), cfg.Team, len(skipped))
	return items, skipped, nil
}
