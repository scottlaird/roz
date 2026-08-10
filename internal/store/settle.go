package store

import (
	"context"
	"fmt"
	"strings"
)

// Settled is one action closed because its predicate came true.
type Settled struct {
	Action *Action
	// PR is the pull request that satisfied it.
	PR string
	// Result is the cascade closing it produced.
	Result *CloseResult
}

// Settle closes every open action whose predicate is now satisfied.
//
// This is what makes the queue mechanical rather than merely modelled. A
// predicate verb says how its action closes; until something asks, the answer
// changes and the queue does not.
//
// It runs after sync, on everything tracked rather than only on what moved:
// an action created since the last poll can be satisfied by an observation
// made before it existed, and would otherwise wait for the pull request to
// change again.
//
// Closing runs the full cascade, so a chain settles as far as it can in one
// call — merging a pull request that was already approved closes wait_review
// and then merge. The loop is bounded by the number of candidates, since each
// pass must close at least one to continue.
func (s *Store) Settle(ctx context.Context, actor Actor) ([]Settled, error) {
	if actor.writes() != Authored {
		return nil, fmt.Errorf("%s may not close actions: closing writes authored fields", actor)
	}

	var settled []Settled
	for {
		candidates, err := s.satisfiedActions(ctx)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			return settled, nil
		}

		var closedAny bool
		for _, c := range candidates {
			result, err := s.CloseAction(ctx, actor, CloseRequest{
				ID: c.action.ID, Reason: ClosedCompleted,
			})
			if err != nil {
				return nil, fmt.Errorf("closing %s on %s: %w", c.action.ID, c.action.Verb, err)
			}
			settled = append(settled, Settled{Action: result.Closed, PR: c.pr, Result: result})
			closedAny = true
		}
		if !closedAny {
			return settled, nil
		}
	}
}

// candidate is an open action that closes on a predicate, with the pull
// request the predicate reads.
type candidate struct {
	action *Action
	pr     string
}

// satisfiedActions returns the open predicate-verb actions whose predicate
// holds.
//
// State is not part of the test. An action blocked behind another still
// closes if its work is demonstrably done: a merged pull request means the
// merge happened, whatever the graph expected to come first. Reality outranks
// the plan, and leaving it open would mean the queue disagreeing with GitHub.
func (s *Store) satisfiedActions(ctx context.Context) ([]candidate, error) {
	tx, err := s.Begin(ctx, ActorHuman)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	pending, err := tx.pendingPredicateActions(ctx)
	if err != nil {
		return nil, err
	}

	var satisfied []candidate
	for _, c := range pending {
		verb, err := tx.LoadVerb(ctx, c.action.Verb)
		if err != nil {
			return nil, err
		}
		predicate, ok := verb.Predicate()
		if !ok {
			continue
		}
		pr, err := tx.LoadPR(ctx, c.pr)
		if err != nil {
			return nil, err
		}
		if predicate(pr) {
			satisfied = append(satisfied, c)
		}
	}
	return satisfied, nil
}

// pendingPredicateActions reads the open actions that close on a predicate
// and have a subject pull request for it to read.
//
// A predicate verb with no subject can never close on its own. That is not an
// error — an action may be linked later — so it is simply not a candidate.
func (t *Tx) pendingPredicateActions(ctx context.Context) ([]candidate, error) {
	fields, err := fieldsOfStruct(&Action{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = "a." + f.column
	}

	query := fmt.Sprintf(`
		SELECT %s, link.pr_id
		FROM action a
		JOIN actionverb v ON v.verb = a.verb
		JOIN action_pr link ON link.action_id = a.id AND link.role = ?
		WHERE a.closed_at IS NULL AND v.closes = ?
		ORDER BY a.n`, strings.Join(columns, ", "))

	rows, err := t.tx.QueryContext(ctx, query, RoleSubject, ClosesPredicate)
	if err != nil {
		return nil, fmt.Errorf("reading actions waiting on a predicate: %w", err)
	}
	defer rows.Close()

	var pending []candidate
	for rows.Next() {
		var a Action
		var pr string
		dest := make([]any, 0, len(fields)+1)
		for _, f := range fields {
			dest = append(dest, f.pointerOf(&a))
		}
		dest = append(dest, &pr)

		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading actions waiting on a predicate: %w", err)
		}
		pending = append(pending, candidate{action: &a, pr: pr})
	}
	return pending, rows.Err()
}
