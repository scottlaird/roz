package github

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNotImplemented is returned by TeamMembers until it is written.
//
// A distinct error rather than a panic or a silent empty result: an empty
// membership table is a legitimate answer — an organisation whose teams happen
// to have no members visible to this token — and it would make every question
// about teams quietly answer "still needed". Failing loudly is the only safe
// placeholder.
var ErrNotImplemented = errors.New("not implemented")

// TeamMembers returns the logins in each of the named teams.
//
// # Why this shape
//
// The reasoning layer wants a pure lookup — codeowners.Teams takes a user and
// returns their teams, with no context and no error — because it is called per
// file while reducing, and threading a network call through that would make
// every reduction fallible and ordered. So membership is fetched once, up
// front, and snapshotted:
//
//	teams := file.Teams()                            // which teams matter
//	members, err := client.TeamMembers(ctx, teams)   // one fetch
//	approved := codeowners.Approval(reviewers, codeowners.NewStaticTeams(members))
//
// The input is the teams a CODEOWNERS actually names, not an organisation.
// Fetching every team in an org to answer a question about four of them is the
// wrong shape, and the file already says which four.
//
// The returned map is keyed by the team as written — "org/slug" — because that
// is the form NewStaticTeams expects and the form the file uses. Values are
// bare logins, as a review reports them.
//
// # Implementing it
//
// Teams belong to organisations, so group the input by org and ask in one
// query with aliases, the way buildQuery and the CODEOWNERS lookup already do:
//
//	query {
//	  o0: organization(login: "acme") {
//	    t0: team(slug: "platform") {
//	      members(first: 100, after: null) {
//	        pageInfo { hasNextPage endCursor }
//	        nodes { login }
//	      }
//	    }
//	    t1: team(slug: "storage") { ... }
//	  }
//	}
//
// Three things to get right, in rough order of how easily they are missed:
//
//   - Membership needs the read:org scope, which a token holding only repo
//     does not have. gh reports that as a 404 on the organisation rather than
//     as a permission error, so the failure looks like "no such org".
//   - A team of more than 100 needs paging, the way Change pages files. An
//     incomplete list here is worse than a slow one: a missing member means a
//     reviewer's approval silently fails to satisfy their team.
//   - A team that does not resolve — renamed, deleted, or invisible to this
//     token — should be reported and not treated as empty, for the same
//     reason. Nulling one alias does not fail the query, so the response has
//     to be checked per team rather than as a whole.
//
// Nested teams are deliberately out of scope, matching the rest of this: a
// parent team's members are its own, and GitHub's own CODEOWNERS resolution
// treats a child team's members as satisfying the parent. Whoever needs that
// can ask for members(membership: ALL) and say so here.
func (c *Client) TeamMembers(ctx context.Context, teams []string) (map[string][]string, error) {
	if len(teams) == 0 {
		return map[string][]string{}, nil
	}
	byOrg, err := groupTeamsByOrg(teams)
	if err != nil {
		return nil, err
	}

	// Everything above this line is the part that does not need the network,
	// and is tested. What is missing is the query and its decoding.
	_ = byOrg
	_ = ctx
	return nil, fmt.Errorf("looking up team members: %w", ErrNotImplemented)
}

// groupTeamsByOrg sorts "org/slug" references into a query plan: one entry per
// organisation, with the slugs wanted from it.
//
// Separate from the fetch so the awkward half — which is the parsing, not the
// HTTP — can be written and tested before the network part exists.
func groupTeamsByOrg(teams []string) (map[string][]string, error) {
	byOrg := map[string][]string{}
	for _, team := range teams {
		ref := strings.TrimPrefix(strings.TrimSpace(team), "@")
		org, slug, found := strings.Cut(ref, "/")
		if !found || org == "" || slug == "" {
			return nil, fmt.Errorf("%q is not a team: want org/slug", team)
		}
		byOrg[org] = append(byOrg[org], slug)
	}
	for org := range byOrg {
		sort.Strings(byOrg[org])
		byOrg[org] = dedupe(byOrg[org])
	}
	return byOrg, nil
}

// dedupe removes repeats from a sorted slice. A CODEOWNERS naming the same
// team on twenty lines should be asked about once.
func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
