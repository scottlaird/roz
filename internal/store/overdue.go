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
	// Raised is the action put in the queue about this wait, or nil when one
	// was already open. An exception nobody sees is the thing this fixes, so
	// the caller reports what it produced.
	Raised *Action
	// Deadline is when it stopped being reasonable.
	Deadline string
	// Waiting is how long it has been, rounded to whole days for a message
	// nobody has to parse.
	Waiting int
	// RankClass is the verb's, and decides whether this needed an item of its
	// own: the queue leaves waits out, and shows everything else.
	RankClass string
}

// InQueue reports whether the action is already something a person will meet
// without being told.
//
// The same test `--unblocked` makes, deliberately: an item is invisible in the
// queue exactly when its rank class is wait, so that is exactly when an
// overdue report has to produce something else to look at.
func (o Overdue) InQueue() bool { return o.RankClass != RankWait }

// overdueQuery finds open actions past their deadline that have not already
// been reported for this wait.
//
// The deadline is coalesced rather than stored: the action's own
// okay_to_wait_until if it has one, otherwise the latest of when it was
// created, when it last became actionable, and when its subject was first
// waiting, plus the verb's wait_days. A verb with no wait_days can never be
// overdue, which is what the join's NOT NULL enforces.
//
// # Why the latest of three clocks, and not waiting_since
//
// The three answer different questions and each is wrong alone:
//
//   - waiting_since is when reviewers could first have seen the pull request.
//     Right for wait_review, and the common case — but it is written for every
//     action sharing that subject, so a merge step created against a pull
//     request already two days in review inherited a deadline in the past.
//   - created_at catches an action nothing has observed a wait for. On its own
//     it makes a hidden chain step overdue on its own birthday, while there is
//     still nothing to do about it.
//   - ready_since is when the action last became actionable. NULL until it is
//     un-hidden or freed, which is why it is folded in rather than preferred.
//
// Taking the latest means a clock can only ever push the deadline out, so no
// combination of them produces an action that is late before it is actionable.
// Each is passed through datetime() first: they arrive in different precisions
// — GitHub's timestamps carry no fraction, the store's carry milliseconds —
// and comparing those as strings puts "…:58Z" after "…:58.500Z".
//
// hidden_behind is excluded outright, not folded into the clock. An action
// folded out of the queue has, by construction, nothing to do about it other
// than clearing the one in front, so it cannot be late in any sense a person
// can act on. Being ready is a state, so blocked and snoozed are already out
// via state below.
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
                         datetime(max(datetime(a.created_at),
                                      datetime(coalesce(a.ready_since, a.created_at)),
                                      datetime(coalesce(a.waiting_since, a.created_at))),
                                  '+' || v.wait_days || ' days'))) AS deadline,
       v.rank_class
  FROM action a
  JOIN actionverb v ON v.verb = a.verb
 WHERE a.closed_at IS NULL
   AND a.state = ?
   AND a.hidden_behind IS NULL
   AND (v.wait_days IS NOT NULL OR a.okay_to_wait_until IS NOT NULL)
   AND deadline < datetime(?)
%s
 ORDER BY deadline, a.n`

// unreported is the clause that makes OverdueWaits idempotent, and the only
// difference between finding what is late and finding what is newly late.
const unreported = `   AND NOT EXISTS (
         SELECT 1 FROM event e
          WHERE e.subject_type = 'action' AND e.subject_id = a.id
            AND e.kind = ? AND datetime(e.at) >= deadline)`

// LateActions returns how many days past its deadline each open action is.
//
// Read-only, and it asks about everything late rather than everything newly
// late: a listing shows a state, where reporting announces a change.
//
// It exists because an action already in the queue is told about by marking it
// rather than by raising a second item — so without this the allowance on a
// verb like `merge` would mean nothing anybody could see, which is the failure
// the whole mechanism is meant to prevent.
func (s *Store) LateActions(ctx context.Context) (map[string]int, error) {
	now := s.now()
	query := fmt.Sprintf(overdueQuery, strings.Join([]string{"a.id"}, ", "), "")
	rows, err := s.db.QueryContext(ctx, query, ActionReady, now.UTC().Format(timeFormat))
	if err != nil {
		return nil, fmt.Errorf("finding late actions: %w", err)
	}
	defer rows.Close()

	late := map[string]int{}
	for rows.Next() {
		var id, deadline, rankClass string
		if err := rows.Scan(&id, &deadline, &rankClass); err != nil {
			return nil, fmt.Errorf("finding late actions: %w", err)
		}
		late[id] = daysPast(deadline, now)
	}
	return late, rows.Err()
}

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
	query := fmt.Sprintf(overdueQuery, strings.Join(columns, ", "), unreported)
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
		var rankClass string
		dest = append(dest, &deadline, &rankClass)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("finding overdue waits: %w", err)
		}
		overdue = append(overdue, Overdue{
			Action:    &a,
			Deadline:  deadline,
			Waiting:   daysSince(waitStartedAt(&a), now),
			RankClass: rankClass,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finding overdue waits: %w", err)
	}

	for i := range overdue {
		if err := s.reportOverdue(ctx, actor, overdue[i]); err != nil {
			return nil, err
		}
		// Only for the ones the queue leaves out. An action already in the
		// queue is already something a person meets; adding a second row about
		// it is two items for one piece of work, and the second cannot be
		// cleared by doing the first.
		//
		// This is why the test is the rank class rather than how the verb
		// closes. `merge` closes on a predicate and sits in the queue like any
		// other item, so an overdue merge needs no twin either.
		if overdue[i].InQueue() {
			continue
		}
		// The log has it either way; this is what makes it something a person
		// meets rather than something they have to go looking for.
		raised, err := s.RaiseAction(ctx, EventWaitedTooLong, overdue[i].Action,
			fmt.Sprintf("chase %s", overdue[i].Action.ID),
			fmt.Sprintf("%s has been waiting %d days, past %s.",
				overdue[i].Action.ID, overdue[i].Waiting, overdue[i].Deadline))
		if err != nil {
			return nil, err
		}
		if raised != nil {
			overdue[i].Raised = raised.Action
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

// sqliteTime is what datetime() returns: no T, no zone, no fraction. The
// deadline is computed in SQL rather than stored, so it comes back in this
// shape rather than in the store's own format.
const sqliteTime = "2006-01-02 15:04:05"

// daysPast is how many whole days a deadline has been missed by, which is zero
// for one missed an hour ago. Presence in the map is what says late; this only
// says by how much.
func daysPast(deadline string, now time.Time) int {
	passed, err := time.Parse(sqliteTime, deadline)
	if err != nil {
		return 0
	}
	days := int(now.UTC().Sub(passed).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

// waitStartedAt is when the clock started: the latest of the three the query
// takes, so the "waiting N days" in a message agrees with the deadline that
// produced it. Reporting a longer wait than the deadline was measured from is
// how an item reads as more overdue than it is.
func waitStartedAt(a *Action) string {
	since := a.CreatedAt
	if a.ReadySince.Valid && a.ReadySince.String > since {
		since = a.ReadySince.String
	}
	if a.WaitingSince.Valid && a.WaitingSince.String > since {
		since = a.WaitingSince.String
	}
	return since
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
