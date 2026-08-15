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
	return s.settle(ctx, actor, nil)
}

// SettleAction closes one action if its predicate now holds, and then anything
// that closing it freed.
//
// For a fact that arrives from a command rather than from a poll: linking a
// subject pull request is exactly the moment a predicate becomes answerable,
// and the action should not wait for the next sync to be asked.
//
// Scoped to the action named and what its closure unblocks, rather than
// running the full sweep. Those are the consequences of what the caller just
// did; everything else on that pull request is sync's job, and closing it here
// would be a targeted command quietly doing a broad one.
//
// A pull request nothing has observed settles nothing: every predicate is
// false where there is no observation, so this is a no-op rather than a
// verdict.
func (s *Store) SettleAction(ctx context.Context, actor Actor, id string) ([]Settled, error) {
	return s.settle(ctx, actor, map[string]bool{id: true})
}

// settle is the shared loop. A nil scope means everything, which is what sync
// wants; a non-nil one grows as closures unblock further actions.
func (s *Store) settle(ctx context.Context, actor Actor, scope map[string]bool) ([]Settled, error) {
	if actor.writes() != Authored {
		return nil, fmt.Errorf("%s may not close actions: closing writes authored fields", actor)
	}

	var settled []Settled
	for {
		candidates, err := s.satisfiedActions(ctx, scope)
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
			// What this closure freed is a consequence of the caller's act, so
			// it stays in scope. A freed successor whose predicate already
			// holds should not have to wait for a poll either.
			for _, freed := range result.Unblocked {
				if scope != nil {
					scope[freed.ID] = true
				}
			}
		}
		if !closedAny {
			return settled, nil
		}
	}
}

// candidate is an open action that closes on a predicate, with enough to find
// what the predicate reads.
type candidate struct {
	action *Action
	// pr is the subject pull request, empty where there is none.
	pr string
	// hasWait says the action is waiting on a ref, so the wait and the
	// repository's refs are worth loading.
	hasWait bool
}

// satisfiedActions returns the open predicate-verb actions whose predicate
// holds.
//
// State is not part of the test. An action blocked behind another still
// closes if its work is demonstrably done: a merged pull request means the
// merge happened, whatever the graph expected to come first. Reality outranks
// the plan, and leaving it open would mean the queue disagreeing with GitHub.
func (s *Store) satisfiedActions(ctx context.Context, scope map[string]bool) ([]candidate, error) {
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
		if scope != nil && !scope[c.action.ID] {
			continue
		}
		verb, err := tx.LoadVerb(ctx, c.action.Verb)
		if err != nil {
			return nil, err
		}
		predicate, ok := verb.Predicate()
		if !ok {
			continue
		}
		facts, err := tx.factsFor(ctx, c)
		if err != nil {
			return nil, err
		}
		if predicate(facts) {
			satisfied = append(satisfied, c)
		}
	}
	return satisfied, nil
}

// factsFor gathers the rows a predicate is allowed to read.
//
// Loaded here rather than inside the predicate so that predicates stay pure
// functions over stored state: the alternative is every predicate holding a
// transaction, and a rule that can query is a rule that can be slow, or
// wrong, in ways a test of the rule alone would not show.
func (t *Tx) factsFor(ctx context.Context, c candidate) (Facts, error) {
	facts := Facts{Action: c.action}

	// A step waiting for one group needs to know who is in it, and the
	// predicate may not ask GitHub. Read here for the group this action names,
	// rather than every group: what a predicate reads should be as narrow as
	// what it asks.
	if c.action.WaitingFor.Valid && c.action.WaitingFor.String != "" {
		members, err := t.MembersOf(ctx, c.action.WaitingFor.String)
		if err != nil {
			return Facts{}, err
		}
		facts.Members = members
	}
	if c.pr != "" {
		pr, err := t.LoadPR(ctx, c.pr)
		if err != nil {
			return Facts{}, err
		}
		facts.PR = pr
	}
	if c.hasWait {
		wait, err := t.RefWaitFor(ctx, c.action.ID)
		if err != nil {
			return Facts{}, err
		}
		if wait != nil {
			facts.Wait = wait
			refs, err := t.RefsIn(ctx, wait.RepoID, wait.Kind)
			if err != nil {
				return Facts{}, err
			}
			facts.Refs = refs
		}
	}
	return facts, nil
}

// pendingPredicateActions reads the open actions that close on a predicate
// and have something for it to read — a subject pull request, a ref wait, or
// both.
//
// A predicate verb with neither can never close on its own. That is not an
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
		SELECT %s, coalesce(link.pr_id, ''), w.action_id IS NOT NULL
		FROM action a
		JOIN actionverb v ON v.verb = a.verb
		LEFT JOIN action_pr link ON link.action_id = a.id AND link.role = ?
		LEFT JOIN action_ref_wait w ON w.action_id = a.id
		WHERE a.closed_at IS NULL AND v.closes = ?
		  AND (link.pr_id IS NOT NULL OR w.action_id IS NOT NULL)
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
		var hasWait bool
		dest := make([]any, 0, len(fields)+2)
		for _, f := range fields {
			dest = append(dest, f.pointerOf(&a))
		}
		dest = append(dest, &pr, &hasWait)

		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading actions waiting on a predicate: %w", err)
		}
		pending = append(pending, candidate{action: &a, pr: pr, hasWait: hasWait})
	}
	return pending, rows.Err()
}
