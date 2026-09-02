package store

import (
	"context"
	"database/sql"
	"fmt"
)

// EventSnoozeExpired is logged when a deferral's date arrives and the action
// or project comes back into play.
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

// Woken is what one WakeExpired sweep brought back into play.
type Woken struct {
	Actions  []*Action
	Projects []*Project
}

// Empty reports whether the sweep found nothing due.
func (w Woken) Empty() bool { return len(w.Actions) == 0 && len(w.Projects) == 0 }

// WakeExpired brings back every action and project whose snooze has run out.
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
// Waking clears the date and the reason, which is what `action wake` and
// `project wake` do by hand. The row stops being a deferral because the
// deferral is over; leaving the columns would make it read as one whose date
// has passed for ever, and "ready with a snooze date" is a combination nothing
// else expects. What the deferral said is in the log — the field changes, and
// the snooze_expired note repeats the date and the reason, so "why did this
// just reappear" is answered by one line of history.
//
// This is the rule, and the queue's expired branch (inPlay) is the fallback
// under it. That branch used to be the rule, and refused to wake for two
// reasons: a clock would be rewriting an authored column, and the reason would
// be lost. Both were real. A deferral that nothing ends is how work goes quiet,
// which is what #264 weighed against them; the reason is kept by the note.
//
// Blocked rather than ready or active where something still blocks it: waking
// is the end of the deferral, not a claim that the work is now actionable, and
// the two applyBlockedState functions are what know the difference.
func (s *Store) WakeExpired(ctx context.Context, actor Actor) (Woken, error) {
	now := s.now().UTC().Format(timeFormat)

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return Woken{}, err
	}
	defer tx.Rollback()

	var woken Woken

	// The shared definition of expired, so this and the queue cannot disagree
	// about which rows are due.
	due, err := tx.loadActions(ctx,
		"SELECT %s FROM action a WHERE a.closed_at IS NULL AND "+expiredSnooze+" ORDER BY a.n",
		ActionSnoozed, now)
	if err != nil {
		return Woken{}, fmt.Errorf("finding expired snoozes: %w", err)
	}
	for _, a := range due {
		after := a.Clone()
		after.State = ActionReady
		after.SnoozeUntil = sql.NullString{}
		after.SnoozeReason = ""
		if _, err := tx.Update(ctx, a, after); err != nil {
			return Woken{}, err
		}
		// After the update, so it reads the row as it now stands: an action
		// with an open blocker goes back to blocked rather than to the queue.
		if err := tx.applyBlockedState(ctx, after); err != nil {
			return Woken{}, err
		}
		// emit rather than Note or Exception: this needs a kind of its own so
		// the two endings can be told apart, and it is not a problem.
		if err := tx.emit(ctx, after, event{
			kind: EventSnoozeExpired,
			note: expiredNote(a.SnoozeUntil.String, a.SnoozeReason),
		}); err != nil {
			return Woken{}, err
		}
		woken.Actions = append(woken.Actions, after)
	}

	dueProjects, err := tx.loadProjects(ctx,
		"SELECT %s FROM project p WHERE "+expiredProjectSnooze+" ORDER BY p.n",
		ProjectSnoozed, now)
	if err != nil {
		return Woken{}, fmt.Errorf("finding expired project snoozes: %w", err)
	}
	for _, p := range dueProjects {
		after := p.Clone()
		after.Status = ProjectActive
		after.SnoozeUntil = sql.NullString{}
		after.SnoozeReason = ""
		if _, err := tx.Update(ctx, p, after); err != nil {
			return Woken{}, err
		}
		if err := tx.applyProjectBlockedState(ctx, after); err != nil {
			return Woken{}, err
		}
		if err := tx.emit(ctx, after, event{
			kind: EventSnoozeExpired,
			note: expiredNote(p.SnoozeUntil.String, p.SnoozeReason),
		}); err != nil {
			return Woken{}, err
		}
		woken.Projects = append(woken.Projects, after)
	}

	if woken.Empty() {
		return woken, nil
	}
	if err := tx.Commit(); err != nil {
		return Woken{}, err
	}
	return woken, nil
}

// expiredNote says what ran out. The reason is included because it is the
// one thing the wake clears that the field-change events do not put in front
// of a reader: the log shows snooze_reason going to "", and the old value is
// there, but the note is the line somebody reads to learn why the item is
// back, and it should not send them looking.
func expiredNote(until, reason string) string {
	note := "the deferral ran out; it was snoozed until " + until
	if reason != "" {
		note += " (" + reason + ")"
	}
	return note
}
