package store

import (
	"context"
	"fmt"
	"strings"
)

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
// # Why only a ready step is asked about
//
// A pipeline holds its steps with action_blocks, not hidden_behind: instantiate
// calls AddBlocker and nothing else. So the ordinary chain on a draft pull
// request is undraft ready, send_for_review blocked, wait_review blocked -- and
// the wait at the end raised "nobody was asked for this review" every sweep,
// about a pull request that has not been sent out yet, which is the correct
// state for a draft.
//
// The advice was worse than unactionable. Running `pr announce` on an unsent
// pull request records an announcement that never happened and starts the wait
// clock, so a later real one looks like a duplicate and every elapsed figure
// after it is wrong.
//
// state is the test, and it is already the answer. An action is blocked exactly
// while it has an open blocker -- applyBlockedState maintains that on every
// edge change and every close -- so `ready` means nothing ahead of it is open,
// by induction along the chain. #265 asks for a recursive walk to the head,
// terminating safely on a cycle; there is nothing to walk and no cycle to
// terminate on, because the column holds the result and the state machine is
// what keeps it true. A second derivation could only disagree with the first.
//
// hidden_behind as well, since a hidden action can be ready: hiding is the
// judgement that there is nothing to do but clear the one in front, which is
// the same argument in a different edge.
//
// Both together are exactly what overdueQuery tests, and that is the point.
// Two sweeps asking "is this worth somebody's attention" that disagree about
// which actions are live means one of them is wrong -- and #262 fixed the
// hidden half of this while leaving the half that actually fires, because a
// chain is blocked rather than hidden and the report said otherwise.
//
// Suppressing is not latching. Nothing is recorded while the step is unready,
// so the exception fires the moment the chain opens up and the wait is
// genuinely stalled, which is the case it was written for.
const unannouncedWaits = `
SELECT %s, pr.id
  FROM action a
  JOIN actionverb v ON v.verb = a.verb
  JOIN action_pr link ON link.action_id = a.id AND link.role = ?
  JOIN pr ON pr.id = link.pr_id
  JOIN github_repo r ON r.id = pr.repo
 WHERE ` + liveAction + `
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
		RoleSubject, ActionReady, PredicateApproved, PredicateApprovedBy, PredicateAnnounced)
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
