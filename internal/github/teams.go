package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

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
// # What it costs to get wrong
//
// Three hazards, in rough order of how easily they are missed:
//
//   - Membership needs the read:org scope, which a token holding only repo
//     does not have. GitHub reports that as a null organisation rather than as
//     a permission error, so the failure reads as "no such org" — hence the
//     error here says so outright.
//   - A team of more than 100 pages. An incomplete list is worse than a slow
//     one: a missing member means a reviewer's approval silently fails to
//     satisfy their team, so running past the bound is an error rather than a
//     truncation to report.
//   - A team that does not resolve — renamed, deleted, or invisible to this
//     token — is an error and not an empty team. Nulling one alias does not
//     fail the query, so every alias is checked rather than the response as a
//     whole. An empty team is a real answer and is recorded as one.
//
// A caller that cannot read membership is not stuck: every other part of the
// answer is correct without it, so `roz codeowners` reports the gap and carries
// on with whatever --team supplied.
//
// Child teams are included, via membership: ALL. That is GitHub's default, and
// it is also what GitHub's own CODEOWNERS resolution does — a child team's
// members satisfy the parent — so anything narrower would answer "still
// needed" for a reviewer GitHub is perfectly happy with. It is passed
// explicitly because the default is load-bearing here rather than incidental.
func (c *Client) TeamMembers(ctx context.Context, teams []string) (map[string][]string, error) {
	if len(teams) == 0 {
		return map[string][]string{}, nil
	}
	byOrg, err := groupTeamsByOrg(teams)
	if err != nil {
		return nil, err
	}
	plan := teamPlan(byOrg)

	members := make(map[string][]string, len(plan))
	// cursors holds the teams with more to read, by index into plan. A team
	// leaves the map when GitHub says there is no next page.
	cursors := make(map[int]string, len(plan))
	for i := range plan {
		cursors[i] = ""
	}

	for page := 0; len(cursors) > 0; page++ {
		if page >= teamMaxPages {
			return nil, fmt.Errorf("looking up team members: %s still had more after %d pages",
				plan[anyIndex(cursors)], teamMaxPages)
		}

		query, aliases := buildTeamQuery(plan, cursors)
		body, runErr := c.run(ctx, query)

		// Rate limiting means wait, not that the question was wrong.
		if errors.Is(runErr, ErrRateLimited) {
			return nil, runErr
		}
		// Any other run error still gets decoded: gh exits non-zero whenever an
		// alias fails to resolve, and the rest of that response is good. The
		// error only surfaces if the body turns out to be unusable.
		next, err := decodeTeamPage(body, plan, aliases, members)
		if err != nil {
			if runErr != nil {
				return nil, fmt.Errorf("looking up team members: %w", runErr)
			}
			return nil, err
		}
		cursors = next
	}
	return members, nil
}

// teamPage caps one round trip. GitHub will not return more than 100 nodes
// from a connection.
const teamPage = 100

// teamMaxPages bounds the paging at 10,000 members per team.
//
// Unlike the ref read, hitting this is an error rather than a truncation to
// report: an incomplete membership list means a reviewer's approval silently
// fails to satisfy their team, which is wrong in the direction that costs a
// review round. A team that large is not a CODEOWNERS entry anyone reasons
// about, so failing is both safe and honest.
//
// It also bounds the loop if GitHub ever returns hasNextPage with a cursor
// that does not advance.
const teamMaxPages = 100

// anyIndex returns one key, for naming a team in the bound's error. Which one
// is arbitrary because the bound is a runaway guard rather than a diagnosis.
func anyIndex(cursors map[int]string) int {
	for i := range cursors {
		return i
	}
	return 0
}

// teamRef is one team to look up, and the key it is reported under.
type teamRef struct {
	org  string
	slug string
}

// String is the "org/slug" form: what the caller asked for, what the map is
// keyed by, and what an error should name.
func (t teamRef) String() string { return t.org + "/" + t.slug }

// teamPlan flattens the per-org grouping into a stable ordered list, so
// aliases are positional and a query is deterministic.
func teamPlan(byOrg map[string][]string) []teamRef {
	orgs := make([]string, 0, len(byOrg))
	for org := range byOrg {
		orgs = append(orgs, org)
	}
	sort.Strings(orgs)

	plan := make([]teamRef, 0, len(byOrg))
	for _, org := range orgs {
		for _, slug := range byOrg[org] {
			plan = append(plan, teamRef{org: org, slug: slug})
		}
	}
	return plan
}

// buildTeamQuery renders one aliased query for the teams that still have
// members to read.
//
// Aliased per team rather than nested per organisation: two teams in one org
// page independently, so a shared org alias would have to be rebuilt as each
// team finishes. One flat alias per team costs the same and pages cleanly.
func buildTeamQuery(plan []teamRef, cursors map[int]string) (string, map[string]int) {
	indexes := make([]int, 0, len(cursors))
	for i := range cursors {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)

	aliases := make(map[string]int, len(indexes))
	var b strings.Builder
	b.WriteString("query {\n  rateLimit { cost remaining limit resetAt }\n")
	for _, i := range indexes {
		alias := "tm" + strconv.Itoa(i)
		aliases[alias] = i

		after := "null"
		if cursors[i] != "" {
			after = fmt.Sprintf("%q", cursors[i])
		}
		fmt.Fprintf(&b, `  %s: organization(login: %q) {
    team(slug: %q) {
      members(first: %d, after: %s, membership: ALL) {
        pageInfo { hasNextPage endCursor }
        nodes { login }
      }
    }
  }
`, alias, plan[i].org, plan[i].slug, teamPage, after)
	}
	b.WriteString("}\n")

	return b.String(), aliases
}

// decodeTeamPage reads one round's response into members, and returns the
// teams that still have more.
//
// An alias that came back null is an error rather than an empty team, because
// the two are indistinguishable in the response and only one of them is safe
// to believe. Which half was null decides the message: a whole organisation
// resolving to nothing is what a token without read:org looks like, and that
// is worth saying outright rather than leaving as "no such org".
func decodeTeamPage(body []byte, plan []teamRef, aliases map[string]int, members map[string][]string) (map[int]string, error) {
	var decoded struct {
		Data map[string]*struct {
			Team *struct {
				Members struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Login string `json:"login"`
					} `json:"nodes"`
				} `json:"members"`
			} `json:"team"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decoding team members: %w", err)
	}

	// Sorted, so which team an error names does not depend on map order.
	names := make([]string, 0, len(aliases))
	for alias := range aliases {
		names = append(names, alias)
	}
	sort.Strings(names)

	next := map[int]string{}
	for _, alias := range names {
		ref := plan[aliases[alias]]
		org := decoded.Data[alias]
		if org == nil {
			return nil, fmt.Errorf(
				"organisation %q did not resolve, so %s could not be read: %s"+
					" (a token with only the repo scope reads this as a missing"+
					" organisation; membership needs read:org)",
				ref.org, ref, firstMessage(decoded.Errors))
		}
		if org.Team == nil {
			return nil, fmt.Errorf("team %s did not resolve — renamed, deleted,"+
				" or invisible to this token: %s", ref, firstMessage(decoded.Errors))
		}

		for _, node := range org.Team.Members.Nodes {
			members[ref.String()] = append(members[ref.String()], node.Login)
		}
		// A team with no members is a real answer, and has to be recorded as
		// one: leaving the key absent would read as "not looked up".
		if _, ok := members[ref.String()]; !ok {
			members[ref.String()] = []string{}
		}

		if org.Team.Members.PageInfo.HasNextPage {
			next[aliases[alias]] = org.Team.Members.PageInfo.EndCursor
		}
	}
	return next, nil
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
