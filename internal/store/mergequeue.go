package store

import (
	"context"
	"fmt"
	"strings"
)

// EventLeftMergeQueue is raised when a pull request leaves the merge queue
// without merging.
const EventLeftMergeQueue = "pr_left_merge_queue"

// Ejected is one pull request that left the merge queue unmerged.
type Ejected struct {
	PR string
	// Action is what was put in the queue for it, or nil when something open
	// already covered merging this pull request.
	Action *Action
}

// EjectedFromMergeQueue reports the one in_merge_queue transition worth
// remarking on.
//
// The common true → false is a successful merge, which is the opposite of a
// problem, so the state has to still be OPEN. Keying on the transition alone
// would raise an exception on every pull request that lands.
//
// Both sides must have been observed. A pull request whose in_merge_queue was
// never read has not left anything, and absence is not a fact.
func EjectedFromMergeQueue(before, after *PR) bool {
	was := before.InMergeQueue.Valid && before.InMergeQueue.Bool
	gone := after.InMergeQueue.Valid && !after.InMergeQueue.Bool
	return was && gone && after.State.Valid && after.State.String == PRStateOpen
}

// ReportEjection records that a pull request was ejected, and puts something
// in the queue for it.
//
// An ejected pull request is the quietest way for finished work to stall: it
// looks exactly like one that was never queued — approved, clean, every check
// green — and nothing is waiting on anyone. The log alone would not be enough,
// which is why this also creates an action.
//
// The action is a merge, so it closes itself when the pull request eventually
// lands. That matters more than the verb reading as an instruction: re-queuing
// is not always the answer — a base branch moving, a check re-running, or
// another pull request failing a batch can all eject this one — but merging is
// what the item is ultimately waiting for either way, and an item that cannot
// close on its own is one somebody has to remember to tidy up.
// Two actors, and two transactions, because these are two different claims.
// Observing that the pull request left the queue is sync's: it read it from
// GitHub. Putting an item in the queue about it is not — action.title is
// authored, and sync is refused it, correctly. That consequence is recorded as
// predicate, the same actor that closes an action when its predicate comes
// true, because it is the same kind of act: a fact was observed and the queue
// follows from it mechanically.
//
// The actors are chosen here rather than passed in, for the reason `roz issue
// observe` fixes its own: which of them writes what is not the caller's to
// decide.
func (s *Store) ReportEjection(ctx context.Context, key string) (*Ejected, error) {
	covered, err := s.mergeIsAlreadyQueued(ctx, key)
	if err != nil {
		return nil, err
	}

	// Allocated before the transaction, the way closing allocates the steps
	// it is about to write: a rejected write should not consume a number.
	var action *Action
	if !covered {
		verb, err := s.mergeVerb(ctx)
		if err != nil {
			return nil, err
		}
		action = NewAction(fmt.Sprintf("%s %s", verb.Label, key), verb.Verb)
		if err := s.AllocateAction(ctx, action); err != nil {
			return nil, err
		}
	}

	if err := s.reportLeftQueue(ctx, key); err != nil {
		return nil, err
	}
	if action == nil {
		return &Ejected{PR: key}, nil
	}
	if err := s.queueEjection(ctx, key, action); err != nil {
		return nil, err
	}
	return &Ejected{PR: key, Action: action}, nil
}

// reportLeftQueue logs the observation, as sync.
func (s *Store) reportLeftQueue(ctx context.Context, key string) error {
	tx, err := s.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	pr, err := tx.LoadPR(ctx, key)
	if err != nil {
		return fmt.Errorf("loading %s: %w", key, err)
	}
	if err := tx.Exception(ctx, pr, EventLeftMergeQueue,
		"left the merge queue without merging"); err != nil {
		return err
	}
	return tx.Commit()
}

// queueEjection writes the action, as predicate.
//
// After the exception rather than with it: if this fails, the exception still
// stands, which is the half that must not be lost. An unrecorded ejection is
// the defect; an ejection recorded without an item beside it is merely less
// useful.
func (s *Store) queueEjection(ctx context.Context, key string, action *Action) error {
	tx, err := s.Begin(ctx, ActorPredicate)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, action); err != nil {
		return err
	}
	if err := tx.LinkPR(ctx, action, key, RoleSubject); err != nil {
		return err
	}
	return tx.Commit()
}

// mergeIsAlreadyQueued reports whether an open action already covers merging
// this pull request.
//
// This is what keeps a reshuffling queue from filling the list: a pull request
// ejected and re-queued twice in a morning produces one item, not three. It
// also covers the ordinary case of a pipeline already having a merge step —
// the ejection is news, but the thing to do about it is already on the list.
func (s *Store) mergeIsAlreadyQueued(ctx context.Context, key string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM action a
		JOIN action_pr link ON link.action_id = a.id AND link.role = ?
		JOIN actionverb v ON v.verb = a.verb
		WHERE link.pr_id = ? AND a.closed_at IS NULL AND v.predicate_key = ?`,
		RoleSubject, key, PredicateMerged).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("reading open merge actions for %s: %w", key, err)
	}
	return count > 0, nil
}

// mergeVerb is the vocabulary entry the new action uses, read rather than
// assumed: its label titles the action, and a database that has relabelled it
// should be believed.
//
// Found by predicate rather than by the name "merge", because the predicate is
// what makes the action close itself. A verb spelled differently but closing
// on pr_merged is the right one; a verb called merge that closes on something
// else is not.
func (s *Store) mergeVerb(ctx context.Context) (*ActionVerb, error) {
	tx, err := s.Begin(ctx, ActorHuman)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var verb ActionVerb
	fields, err := fieldsOfStruct(&verb)
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	dest := make([]any, len(fields))
	for i, f := range fields {
		columns[i] = f.column
		dest[i] = f.pointerOf(&verb)
	}

	query := fmt.Sprintf(
		"SELECT %s FROM actionverb WHERE predicate_key = ? AND active = 1",
		strings.Join(columns, ", "))
	if err := tx.tx.QueryRowContext(ctx, query, PredicateMerged).Scan(dest...); err != nil {
		return nil, fmt.Errorf("no active verb closes on %s: %w", PredicateMerged, err)
	}
	return &verb, nil
}
