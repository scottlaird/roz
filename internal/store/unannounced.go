package store

import (
	"context"
	"fmt"
	"strings"
)

// EventWaitingUnannounced is raised when an action is waiting for a review
// that nobody was asked for.
//
// Its own kind rather than a variant of waited_too_long, because the two want
// different responses: a slow review is chased, and a review nobody requested
// is announced. Sending somebody to chase reviewers who were never asked is
// the failure this exists to prevent, and it is what the timeout would
// eventually have said.
const EventWaitingUnannounced = "waiting_unannounced"

// unannouncedWaits finds open waits on a review that was never announced.
//
// Structural rather than timed. The fact is knowable the moment the action
// becomes a wait: GitHub's own review request can arrive from CODEOWNERS with
// nobody told, so a pull request looks requested while nobody has been asked.
// Waiting for days to say so is waiting for the evidence to age, not to
// arrive.
//
// Both waits on approval are covered: the whole pull request's, and one named
// group's. Neither was asked for if nobody was told.
//
// Keyed on the pipeline actually containing an announcement step, by its
// predicate rather than by the verb's name — a chain may call the step
// something else, and what makes it an announcement is what closes it. It is
// what
// keeps it quiet where it would be wrong: a repository whose chain has no
// announcement step never records one, and a team relying on GitHub's own
// notifications has not made a mistake. The pull request's own pipeline wins
// over the repository's, read live, the same rule closing a step follows.
//
// announced_at only. first_review_requested_at being set is exactly what made
// the original case invisible, so treating it as evidence would reproduce the
// bug rather than report it.
//
// # Why hidden steps are excluded
//
// A hidden action is not waiting; it is parked. The ordinary pipeline on a
// draft pull request is undraft, then an announce hidden behind it, then a
// wait_review hidden behind that — and the wait raised "nobody was asked for
// this review" every sweep, for ever, about a pull request that is still a
// draft and has an announce step queued up to do exactly what the exception
// asks for. Two of them fired every sweep for a week. See #262.
//
// This asks whether there is something to go and do, and a hidden action has
// by construction nothing to do but clear the one in front. The same rule
// overdueQuery draws, for the same reason: hiding defers the question rather
// than answering it, so the exception fires the moment the step becomes live.
//
// blocked_by is deliberately not treated the same way. Hidden is the judgement
// that there is nothing to do; blocked is a fact about ordering, and a blocked
// step can have a real announcement gap somebody could close today.
const unannouncedWaits = `
SELECT %s, pr.id
  FROM action a
  JOIN actionverb v ON v.verb = a.verb
  JOIN action_pr link ON link.action_id = a.id AND link.role = ?
  JOIN pr ON pr.id = link.pr_id
  JOIN github_repo r ON r.id = pr.repo
 WHERE a.closed_at IS NULL
   AND a.hidden_behind IS NULL
   AND v.predicate_key IN (?, ?)
   AND pr.announced_at IS NULL
   AND EXISTS (SELECT 1 FROM pipeline_step s
                JOIN actionverb sv ON sv.verb = s.verb
               WHERE s.pipeline = coalesce(pr.pipeline, r.pipeline)
                 AND sv.predicate_key = ?)
 ORDER BY a.n`

// Unannounced is a wait on a review nobody was asked for.
type Unannounced struct {
	Action *Action
	// PR is the pull request that was never announced, which is what somebody
	// has to go and announce.
	PR string
}

// UnannouncedWaits reports every wait whose review was never requested of a
// person, and returns what it reported.
//
// Reported once per action per ExceptionInterval, through the same
// ExceptionOnce every standing condition uses: the condition is true on every
// cycle until somebody acts on it, and a monitor that repeats itself every
// fifteen seconds is a monitor people turn off.
func (s *Store) UnannouncedWaits(ctx context.Context, actor Actor) ([]Unannounced, error) {
	fields, err := fieldsOfStruct(&Action{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = "a." + f.column
	}

	query := fmt.Sprintf(unannouncedWaits, strings.Join(columns, ", "))
	rows, err := s.db.QueryContext(ctx, query,
		RoleSubject, PredicateApproved, PredicateApprovedBy, PredicateAnnounced)
	if err != nil {
		return nil, fmt.Errorf("finding waits on an unannounced review: %w", err)
	}
	defer rows.Close()

	var found []Unannounced
	for rows.Next() {
		var a Action
		var pr string
		dest := make([]any, 0, len(fields)+1)
		for _, f := range fields {
			dest = append(dest, f.pointerOf(&a))
		}
		dest = append(dest, &pr)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("finding waits on an unannounced review: %w", err)
		}
		copied := a
		found = append(found, Unannounced{Action: &copied, PR: pr})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finding waits on an unannounced review: %w", err)
	}
	if len(found) == 0 {
		return nil, nil
	}

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var reported []Unannounced
	for _, wait := range found {
		note := fmt.Sprintf(
			"%s is waiting for a review of %s that nobody was asked for: "+
				"announced_at is empty, so any review request on it came from "+
				"GitHub rather than from a person. `roz pr announce %s` once "+
				"somebody has been told.",
			wait.Action.ID, wait.PR, wait.PR)
		raised, err := tx.ExceptionOnce(ctx, wait.Action, EventWaitingUnannounced, note)
		if err != nil {
			return nil, err
		}
		if raised {
			reported = append(reported, wait)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return reported, nil
}
