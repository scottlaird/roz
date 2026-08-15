package ghsync

import (
	"context"
	"strings"
	"time"

	"github.com/scottlaird/roz/internal/store"
)

// TeamReader reads who belongs to a team.
//
// Separate from Fetcher because it is a separate cost and a separate scope:
// membership needs read:org, which a token holding only repo does not have, so
// a client that cannot answer this still syncs everything else.
type TeamReader interface {
	TeamMembers(ctx context.Context, teams []string) (map[string][]string, error)
}

// membershipTTL is how long a membership answer is trusted.
//
// Membership changes without anything in roz changing, so there is no head to
// key this on the way owners are keyed on the commit — only time. A day is
// chosen against the cost of being wrong in each direction: too old and a step
// waits for an approval that already counted, which somebody notices within a
// day of caring; too fresh and a request is spent on an answer that changes
// perhaps twice a year.
const membershipTTL = 24 * time.Hour

// timeFormat is the store's, because these timestamps are compared against
// stored ones as text.
const timeFormat = "2006-01-02T15:04:05.000Z"

// syncTeams refreshes the membership of the teams an open step waits for.
//
// Bounded by what is actually waiting, not by the organisation. A step that
// waits for one group is the only thing that needs this answered, and there are
// usually two or three of those — so the read is one request per organisation,
// once a day, rather than anything proportional to the repositories tracked.
//
// A team nothing waits for is never read, and a team whose answer is recent is
// left alone. Where nothing is stale this makes no request at all, which is
// what keeps it off the cost of an ordinary cycle.
func syncTeams(ctx context.Context, st *store.Store, reader TeamReader, now time.Time, result *Result) error {
	awaited, err := st.TeamsAwaited(ctx)
	if err != nil {
		return err
	}
	if len(awaited) == 0 {
		return nil
	}
	ages, err := st.TeamMembershipAge(ctx)
	if err != nil {
		return err
	}

	stale := staleTeams(awaited, ages, now)
	if len(stale) == 0 {
		return nil
	}

	// GitHub names a team "org/slug"; CODEOWNERS and everything in roz writes
	// "@org/slug". The @ goes on the way out and comes back on the way in, so
	// one spelling is stored and the other never escapes this function.
	asked := make([]string, 0, len(stale))
	for _, team := range stale {
		asked = append(asked, strings.TrimPrefix(team, "@"))
	}

	members, err := reader.TeamMembers(ctx, asked)
	if err != nil {
		return &readError{err: err}
	}
	result.asked++

	recorded := make(map[string][]string, len(members))
	for team, logins := range members {
		named := make([]string, 0, len(logins))
		for _, login := range logins {
			named = append(named, "@"+login)
		}
		recorded["@"+team] = named
	}
	if err := st.RecordTeamMembers(ctx, store.ActorSyncGitHub, recorded); err != nil {
		return err
	}
	result.Teams = len(recorded)
	return nil
}

// staleTeams is the subset worth asking about: never read, or read longer ago
// than the answer is trusted for.
func staleTeams(awaited []string, ages map[string]string, now time.Time) []string {
	cutoff := now.Add(-membershipTTL).UTC().Format(timeFormat)

	var stale []string
	for _, team := range awaited {
		at, known := ages[team]
		if !known || at < cutoff {
			stale = append(stale, team)
		}
	}
	return stale
}
