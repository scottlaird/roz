package store

import (
	"context"
	"database/sql"
	"fmt"
)

// EventSnoozeExpired is logged when a deferral's date arrives and the action
// comes back into play.
//
// Its own kind rather than the bare state change, because #264 asks for the
// two ways a wait can end to be distinguishable. A wait handed to somebody
// else has two independent ends — the tracker issue closes, or enough time
// passes that it is worth asking how it is going — and they lead to different
// next steps. Collapsing them into one `closed` event throws that away.
//
// info rather than an exception. A deferred action reaching its date is the
// deferral working, not a problem; the reader wanted to be asked about it
// today and now is being asked.
const EventSnoozeExpired = "snooze_expired"

// WakeExpired brings back every action whose snooze has run out.
//
// The date half of a wait with two ends. A snoozed action is invisible to the
// overdue check on purpose — overdueQuery filters on state, because a deferred
// action is not late, which is the whole point of deferring it — so the date
// arriving cannot be an exception. It has to be a wake, which is what this is.
//
// The other end needs nothing: Settle has no state filter, so a snoozed
// wait_issue already closes the moment its issue does, whatever state the
// action is in. That was checked rather than assumed.
//
// Waking clears the date and the reason, which is what `action wake` does by
// hand. The row stops being a deferral because the deferral is over; what it
// said is in the log, and leaving it would make the action read as one whose
// date has passed for ever.
//
// Blocked rather than ready where something still blocks it: waking is the end
// of the deferral, not a claim that the work is now actionable, and
// applyBlockedState is what knows the difference.
func (s *Store) WakeExpired(ctx context.Context, actor Actor) ([]*Action, error) {
	now := s.now().UTC().Format(timeFormat)

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	due, err := tx.loadActions(ctx, `
		SELECT %s FROM action a
		 WHERE a.closed_at IS NULL
		   AND a.state = ?
		   AND a.snooze_until IS NOT NULL
		   AND a.snooze_until <= ?
		 ORDER BY a.n`, ActionSnoozed, now)
	if err != nil {
		return nil, fmt.Errorf("finding expired snoozes: %w", err)
	}
	if len(due) == 0 {
		return nil, nil
	}

	woken := make([]*Action, 0, len(due))
	for _, a := range due {
		after := a.Clone()
		after.State = ActionReady
		after.SnoozeUntil = sql.NullString{}
		after.SnoozeReason = ""
		if _, err := tx.Update(ctx, a, after); err != nil {
			return nil, err
		}
		// After the update, so it reads the row as it now stands: an action
		// with an open blocker goes back to blocked rather than to the queue.
		if err := tx.applyBlockedState(ctx, after); err != nil {
			return nil, err
		}
		// emit rather than Note or Exception: this needs a kind of its own so
		// the two endings can be told apart, and it is not a problem.
		if err := tx.emit(ctx, after, event{
			kind: EventSnoozeExpired,
			note: "the deferral ran out; it was snoozed until " + a.SnoozeUntil.String,
		}); err != nil {
			return nil, err
		}
		woken = append(woken, after)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return woken, nil
}
