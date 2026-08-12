package store

import (
	"context"
	"fmt"
)

// Raised is an action an exception put in the queue.
type Raised struct {
	Action *Action
	// Kind is the exception that raised it.
	Kind string
}

// RaiseVerb is what an exception's action closes on.
//
// Human-closed, deliberately. "I looked at this and it is fine" is a
// legitimate outcome — a wait that has gone long may simply be worth waiting
// for — so closing must not require the condition to have gone away. A
// predicate verb would refuse to close until the world changed, which for
// several of these means never.
//
// `decide` rather than `investigate`: what an exception leaves is a judgement
// about what to do — chase, escalate, accept, stop tracking — and its rank
// class sorts it as one.
const RaiseVerb = "decide"

// RaiseAction puts an action in the queue for a condition, unless one is
// already there.
//
// This is the half of an exception that reaches a person. The log keeps every
// firing; the queue keeps one item per outstanding condition, which is the
// distinction that matters: a rule that re-reports should not multiply what
// there is to do about it.
//
// Returns nil when this condition already raised something. That is the
// ordinary case on a second firing, not a failure.
//
// The actor is predicate rather than whatever observed the condition. Sync may
// not write an action's title — it is authored, correctly — and the act being
// recorded is the same one Settle records: a fact was established and the
// queue follows from it.
func (s *Store) RaiseAction(ctx context.Context, kind string, subject Record, title, why string) (*Raised, error) {
	raised, err := s.conditionWasRaised(ctx, kind, subject)
	if err != nil {
		return nil, err
	}
	if raised {
		return nil, nil
	}

	// Allocated before the transaction, the way closing allocates the steps it
	// is about to write: a rejected write should not consume a number.
	action := NewAction(title, RaiseVerb)
	action.Why = why
	if err := s.AllocateAction(ctx, action); err != nil {
		return nil, err
	}

	tx, err := s.Begin(ctx, ActorPredicate)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, action); err != nil {
		return nil, err
	}
	if _, err := tx.tx.ExecContext(ctx, `
		INSERT INTO raised_action (action_id, kind, subject_type, subject_id, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		action.ID, kind, subject.subjectType(), subject.subjectID(), tx.at); err != nil {
		return nil, fmt.Errorf("recording what raised %s: %w", action.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Raised{Action: action, Kind: kind}, nil
}

// conditionWasRaised reports whether this condition has already produced an
// action, open or closed.
//
// Closed counts, and that is the part worth being deliberate about. Some of
// these conditions persist and re-fire on every poll — a pull request that has
// gone invisible is reported each time sync cannot read it — so a check that
// only looked at open actions would raise a fresh one the moment the last was
// closed. The item would be impossible to clear while the condition lasted,
// which is worse than not having it: an un-closeable entry in a queue teaches
// people to ignore the queue.
//
// The cost is that a condition which genuinely recurs after being dealt with —
// a pull request that reappears and vanishes again — produces nothing new. The
// log still has every firing, and the alternative is a nag with no off switch.
func (s *Store) conditionWasRaised(ctx context.Context, kind string, subject Record) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM raised_action
		WHERE kind = ? AND subject_type = ? AND subject_id = ?`,
		kind, subject.subjectType(), subject.subjectID()).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking what has already been raised for %s: %w",
			subject.subjectID(), err)
	}
	return count > 0, nil
}
