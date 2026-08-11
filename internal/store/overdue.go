package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EventWaitedTooLong is the exception kind raised when a wait has gone on
// longer than its verb allows.
//
// An exception rather than a new alerting path: severity = 'exception' is
// what a monitor already filters on, and inventing a second channel for the
// second thing worth shouting about is how a system ends up with five.
const EventWaitedTooLong = "waited_too_long"

// Overdue is one action that has been waiting longer than it should.
type Overdue struct {
	Action *Action
	// Deadline is when it stopped being reasonable.
	Deadline string
	// Waiting is how long it has been, rounded to whole days for a message
	// nobody has to parse.
	Waiting int
}

// overdueQuery finds open actions past their deadline that have not already
// been reported for this wait.
//
// The deadline is coalesced rather than stored: the action's own
// okay_to_wait_until if it has one, otherwise waiting_since (or created_at,
// where nothing has observed a wait beginning) plus the verb's wait_days. A
// verb with no wait_days can never be overdue, which is what the join's
// NOT NULL enforces.
//
// The last clause is what makes this idempotent without new state. An
// exception already raised at or after the deadline means this wait has been
// reported; the log is the record, so nothing else has to remember. If the
// deadline later moves out — the allowance was raised, or a fresh review was
// requested — the old exception falls before the new deadline and the wait
// can be reported again, which is right: it is a different wait.
const overdueQuery = `
SELECT %s,
       datetime(coalesce(a.okay_to_wait_until,
                         datetime(coalesce(a.waiting_since, a.created_at),
                                  '+' || v.wait_days || ' days'))) AS deadline
  FROM action a
  JOIN actionverb v ON v.verb = a.verb
 WHERE a.closed_at IS NULL
   AND a.state = ?
   AND (v.wait_days IS NOT NULL OR a.okay_to_wait_until IS NOT NULL)
   AND deadline < datetime(?)
   AND NOT EXISTS (
         SELECT 1 FROM event e
          WHERE e.subject_type = 'action' AND e.subject_id = a.id
            AND e.kind = ? AND datetime(e.at) >= deadline)
 ORDER BY deadline, a.n`

// OverdueWaits reports the actions that have been waiting too long, raising
// one exception each.
//
// It is the counterpart of Settle and runs in the same places: something has
// to notice, and nobody is going to read a page to find out. Settle closes
// what has finished; this speaks up about what has not.
//
// Raising rather than changing: an overdue action is not closed, snoozed or
// reprioritised. What to do about it is a judgement, and this is only the
// part that says a judgement is wanted.
func (s *Store) OverdueWaits(ctx context.Context, actor Actor) ([]Overdue, error) {
	fields, err := fieldsOfStruct(&Action{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = "a." + f.column
	}

	// The store's clock, not a parameter: the exception this raises is
	// stamped from the same one, and two clocks that agree in production and
	// diverge under test is a bug waiting for a Tuesday.
	now := s.now()
	at := now.UTC().Format(timeFormat)
	query := fmt.Sprintf(overdueQuery, strings.Join(columns, ", "))
	rows, err := s.db.QueryContext(ctx, query, ActionReady, at, EventWaitedTooLong)
	if err != nil {
		return nil, fmt.Errorf("finding overdue waits: %w", err)
	}
	defer rows.Close()

	var overdue []Overdue
	for rows.Next() {
		var a Action
		var deadline string
		dest := make([]any, 0, len(fields)+1)
		for _, f := range fields {
			dest = append(dest, f.pointerOf(&a))
		}
		dest = append(dest, &deadline)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("finding overdue waits: %w", err)
		}
		overdue = append(overdue, Overdue{
			Action:   &a,
			Deadline: deadline,
			Waiting:  daysSince(waitStartedAt(&a), now),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finding overdue waits: %w", err)
	}

	for i := range overdue {
		if err := s.reportOverdue(ctx, actor, overdue[i]); err != nil {
			return nil, err
		}
	}
	return overdue, nil
}

func (s *Store) reportOverdue(ctx context.Context, actor Actor, o Overdue) error {
	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	note := fmt.Sprintf("%s for %d days, past %s", o.Action.Verb, o.Waiting, o.Deadline)
	if err := tx.Exception(ctx, o.Action, EventWaitedTooLong, note); err != nil {
		return err
	}
	return tx.Commit()
}

// waitStartedAt is when the clock started: what sync observed if it observed
// anything, and otherwise when the action was created.
func waitStartedAt(a *Action) string {
	if a.WaitingSince.Valid && a.WaitingSince.String != "" {
		return a.WaitingSince.String
	}
	return a.CreatedAt
}

// daysSince is whole days, for a message rather than for a comparison — the
// comparison is SQL's, against a real timestamp.
func daysSince(from string, now time.Time) int {
	started, err := time.Parse(timeFormat, from)
	if err != nil {
		return 0
	}
	return int(now.UTC().Sub(started).Hours() / 24)
}

// ObserveWaitingSince records when reviewers could first have seen a pull
// request, on every open action whose subject it is.
//
// This is the one thing that writes action.waiting_since. The sketch called
// it "derivable from review requests" and nothing had derived it, which left
// the column meaning nothing and every wait clock starting from whenever the
// action happened to be created.
//
// An empty value is not a fact: where GitHub has reported no review request,
// the stored value is left alone rather than cleared, the same rule the
// column merge follows.
func (t *Tx) ObserveWaitingSince(ctx context.Context, prID, at string) error {
	if at == "" {
		return nil
	}
	actions, err := t.LinkedActions(ctx, prID)
	if err != nil {
		return err
	}
	for _, a := range actions {
		if a.WaitingSince.Valid && a.WaitingSince.String == at {
			continue
		}
		after := a.Clone()
		after.WaitingSince = sql.NullString{String: at, Valid: true}
		if _, err := t.Update(ctx, a, after); err != nil {
			return err
		}
	}
	return nil
}
