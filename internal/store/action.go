package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Action states.
const (
	ActionReady   = "ready"
	ActionBlocked = "blocked"
	ActionSnoozed = "snoozed"
	ActionDone    = "done"
	ActionDropped = "dropped"
)

// Why an action closed. Keeping these apart is what stops an abandoned item
// looking like a finished one.
const (
	ClosedCompleted  = "completed"
	ClosedSuperseded = "superseded"
	ClosedDropped    = "dropped"
	ClosedObsolete   = "obsolete"
)

var (
	ActionStates  = []string{ActionReady, ActionBlocked, ActionSnoozed, ActionDone, ActionDropped}
	ClosedReasons = []string{ClosedCompleted, ClosedSuperseded, ClosedDropped, ClosedObsolete}
)

// Action is one thing to do next.
//
// Short-lived, and usually advancing some project — though ProjectID is
// nullable, because some actions advance nothing. The queue is what you read;
// the project table is what you plan from.
//
// Verb is load-bearing rather than descriptive: it carries how the action
// closes. A predicate verb closes when its function says the work is done, so
// only the human-closed verbs ever reach the queue as thinking work.
//
// WaitingOn and WaitingSince are observed — which teams, and when they could
// first have seen it — so that being patient and nobody having looked in four
// days stop looking alike.
type Action struct {
	ID   string `db:"id" kind:"identity"`
	Kind string `db:"kind" kind:"identity"`
	N    int64  `db:"n" kind:"identity"`

	Title     string         `db:"title"`
	Verb      string         `db:"verb"`
	State     string         `db:"state"`
	ProjectID sql.NullString `db:"project_id"`

	// Why is what this unblocks. One sentence of non-action text, maximum.
	Why string `db:"why"`

	// HiddenBehind is not the blocked-by edge. Blocked-by is a fact about
	// ordering; hidden-behind is the judgement that there is nothing to do
	// about this one except clear the other, so it folds out of the queue.
	HiddenBehind sql.NullString `db:"hidden_behind"`

	SnoozeUntil  sql.NullString `db:"snooze_until"`
	SnoozeReason string         `db:"snooze_reason"`

	// RankPin overrides the computed sort where it is wrong.
	RankPin sql.NullInt64 `db:"rank_pin"`

	WaitingOn    string         `db:"waiting_on" kind:"observed" format:"json"`
	WaitingSince sql.NullString `db:"waiting_since" kind:"observed"`

	ClosedAt     sql.NullString `db:"closed_at"`
	ClosedReason sql.NullString `db:"closed_reason"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`
}

func (a *Action) table() string       { return "action" }
func (a *Action) subjectType() string { return "action" }
func (a *Action) subjectID() string   { return a.ID }

// NewAction returns an unsaved Action with the defaults a new one takes.
//
// It allocates nothing. Fill in the authored fields, then call
// Store.AllocateAction — in that order, so that rejecting bad input costs no
// identifier.
func NewAction(title, verb string) *Action {
	return &Action{
		Title:     title,
		Verb:      verb,
		State:     ActionReady,
		WaitingOn: "[]",
	}
}

// AllocateAction gives an action its identifier.
//
// The number is consumed as soon as this returns, whether or not the insert
// that follows succeeds. Call it last, once the record is otherwise ready.
func (s *Store) AllocateAction(ctx context.Context, a *Action) error {
	ident, err := s.Allocate(ctx, EntityAction)
	if err != nil {
		return err
	}
	a.ID, a.Kind, a.N = ident.ID, ident.Kind, ident.N
	return nil
}

// LoadAction reads an action by id, returning sql.ErrNoRows if there is none.
func (t *Tx) LoadAction(ctx context.Context, id string) (*Action, error) {
	var a Action
	if err := t.Load(ctx, &a, id); err != nil {
		return nil, err
	}
	return &a, nil
}

// Clone returns a copy to mutate, leaving the original as the before image
// for Tx.Update.
func (a *Action) Clone() *Action {
	clone := *a
	return &clone
}

// IsOpen reports whether the action is still outstanding.
func (a *Action) IsOpen() bool {
	return a.State != ActionDone && a.State != ActionDropped
}

// LoadVerb reads the vocabulary entry this action uses.
func (t *Tx) LoadVerb(ctx context.Context, verb string) (*ActionVerb, error) {
	var v ActionVerb
	fields, err := fieldsOfStruct(&v)
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	dest := make([]any, len(fields))
	for i, f := range fields {
		columns[i] = f.column
		dest[i] = f.pointerOf(&v)
	}

	query := fmt.Sprintf("SELECT %s FROM actionverb WHERE verb = ?", strings.Join(columns, ", "))
	if err := t.tx.QueryRowContext(ctx, query, verb).Scan(dest...); err != nil {
		return nil, err
	}
	return &v, nil
}

// ActionFilter narrows ListActions. The zero value selects everything.
type ActionFilter struct {
	// State keeps actions in one state.
	State string
	// Verb keeps actions using one verb.
	Verb string
	// Project keeps actions advancing one project.
	Project string
	// Open keeps everything not closed.
	Open bool
	// Expired keeps snoozed actions whose date has passed — the query the
	// sketch calls the highest value in the system, because a snooze nobody
	// is watching is how work goes quiet.
	Expired bool
}

// ListActions returns actions matching the filter, ordered by number.
//
// Ordering is on n rather than id, which is the reason n exists: NA100 sorts
// before NA41 lexically. This is creation order, not queue order — ranking
// needs the dependency graph and comes with the queue.
func (s *Store) ListActions(ctx context.Context, filter ActionFilter) ([]*Action, error) {
	fields, err := fieldsOfStruct(&Action{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	where, args := filter.clauses(s.now().UTC().Format(timeFormat))
	query := fmt.Sprintf("SELECT %s FROM action", strings.Join(columns, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY n"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing actions: %w", err)
	}
	defer rows.Close()

	var actions []*Action
	for rows.Next() {
		var a Action
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&a)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing actions: %w", err)
		}
		actions = append(actions, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing actions: %w", err)
	}
	return actions, nil
}

func (f ActionFilter) clauses(now string) ([]string, []any) {
	var where []string
	var args []any

	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if f.Verb != "" {
		where = append(where, "verb = ?")
		args = append(args, f.Verb)
	}
	if f.Project != "" {
		where = append(where, "project_id = ?")
		args = append(args, f.Project)
	}
	if f.Open {
		where = append(where, "closed_at IS NULL")
	}
	if f.Expired {
		where = append(where, "state = ? AND snooze_until IS NOT NULL AND snooze_until < ?")
		args = append(args, ActionSnoozed, now)
	}
	return where, args
}
