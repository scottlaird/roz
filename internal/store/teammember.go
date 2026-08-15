package store

import (
	"context"
	"fmt"
	"strings"
)

// MembersOf returns the logins recorded for a team, as CODEOWNERS writes
// them.
//
// Empty where the team has never been read, which is not the same as an empty
// team — and both answer "nobody in it has approved", because absence is not
// completion. A wait that will not close because membership was never read
// sits visibly in the queue; one that closed for the same reason would not.
func (t *Tx) MembersOf(ctx context.Context, team string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT login FROM team_member WHERE team = ? ORDER BY login", team)
	if err != nil {
		return nil, fmt.Errorf("reading the members of %s: %w", team, err)
	}
	defer rows.Close()

	var logins []string
	for rows.Next() {
		var login string
		if err := rows.Scan(&login); err != nil {
			return nil, fmt.Errorf("reading the members of %s: %w", team, err)
		}
		logins = append(logins, login)
	}
	return logins, rows.Err()
}

// SetTeamMembers replaces what is known about one team.
//
// Replaces rather than merges: a member who left has to disappear, and a
// membership list is small enough that rewriting it is simpler than working
// out the difference. The read time moves on every write, including one that
// changes nothing, because what it records is when the answer was checked.
func (t *Tx) SetTeamMembers(ctx context.Context, team string, logins []string) error {
	if !strings.Contains(team, "/") {
		return fmt.Errorf("%s is a person, not a team: only a team has members", team)
	}
	if _, err := t.tx.ExecContext(ctx,
		"INSERT INTO team_membership (team, synced_at) VALUES (?, ?) "+
			"ON CONFLICT(team) DO UPDATE SET synced_at = excluded.synced_at",
		team, t.at); err != nil {
		return fmt.Errorf("recording that %s was read: %w", team, err)
	}
	if _, err := t.tx.ExecContext(ctx,
		"DELETE FROM team_member WHERE team = ?", team); err != nil {
		return fmt.Errorf("clearing the members of %s: %w", team, err)
	}
	for _, login := range logins {
		if login == "" {
			continue
		}
		if _, err := t.tx.ExecContext(ctx,
			"INSERT INTO team_member (team, login) VALUES (?, ?)",
			team, login); err != nil {
			return fmt.Errorf("writing a member of %s: %w", team, err)
		}
	}
	return nil
}

// TeamsAwaited lists the teams an open step is waiting for a review from.
//
// This is what bounds the membership read. Fetching every team in an
// organisation to answer a question about three of them is the wrong shape,
// and the open steps already say which three.
//
// Only actions whose verb needs an owner. Anything else with waiting_for set
// is a note to a reader — #84's authored answer to "which of these reviewers
// are we actually waiting for" — and does not need membership to be resolved,
// so reading it would put teams nothing is blocked on into the poll.
func (s *Store) TeamsAwaited(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT a.waiting_for FROM action a
		JOIN actionverb v ON v.verb = a.verb
		WHERE a.closed_at IS NULL
		  AND v.requires_owner = 1
		  AND a.waiting_for IS NOT NULL
		  AND instr(a.waiting_for, '/') > 0
		ORDER BY a.waiting_for`)
	if err != nil {
		return nil, fmt.Errorf("reading the teams being waited on: %w", err)
	}
	defer rows.Close()

	var teams []string
	for rows.Next() {
		var team string
		if err := rows.Scan(&team); err != nil {
			return nil, fmt.Errorf("reading the teams being waited on: %w", err)
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

// TeamMembershipAge returns when each team was last read, by team. A team
// absent from the map has never been read — which includes a team read and
// found empty only if nothing recorded the read, and nothing does that.
func (s *Store) TeamMembershipAge(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT team, synced_at FROM team_membership")
	if err != nil {
		return nil, fmt.Errorf("reading team membership ages: %w", err)
	}
	defer rows.Close()

	ages := map[string]string{}
	for rows.Next() {
		var team, at string
		if err := rows.Scan(&team, &at); err != nil {
			return nil, fmt.Errorf("reading team membership ages: %w", err)
		}
		ages[team] = at
	}
	return ages, rows.Err()
}

// RecordTeamMembers writes what GitHub said about several teams, in one unit
// of work.
//
// A team GitHub answered for with nobody in it is still recorded as read. An
// empty team is a real answer — the members left, or the token cannot see
// them — and treating it as unread would fetch it again every cycle.
func (s *Store) RecordTeamMembers(ctx context.Context, actor Actor, members map[string][]string) error {
	if len(members) == 0 {
		return nil
	}
	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for team, logins := range members {
		if err := tx.SetTeamMembers(ctx, team, logins); err != nil {
			return err
		}
	}
	return tx.Commit()
}
