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

// Orderings a list can be returned in.
//
// The default everywhere is creation order, which is honest about being
// arbitrary. OrderPriority is the first step towards the ranking the sketch
// describes; it is not that ranking, which also wants rank_class,
// unblocks_count and effort.
const (
	OrderCreated  = ""
	OrderPriority = "priority"
)

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
	// Unblocked keeps what could be worked on right now: open, ready, and
	// not folded out of the queue behind something else. It is the queue.
	//
	// It reads state rather than counting blockers, because state is
	// recomputed from the open blockers wherever an edge or a closure moves
	// it — two ways of answering the same question would be one too many.
	Unblocked bool
	// Order is how the results come back. Empty is creation order.
	Order string
	// Expired keeps snoozed actions whose date has passed — the query the
	// sketch calls the highest value in the system, because a snooze nobody
	// is watching is how work goes quiet.
	Expired bool
}

// ListActions returns actions matching the filter.
//
// Creation order by default, and ordering is on n rather than id, which is
// the reason n exists: NA100 sorts before NA41 lexically. OrderPriority sorts
// by what the action advances instead — see actionOrder.
func (s *Store) ListActions(ctx context.Context, filter ActionFilter) ([]*Action, error) {
	fields, err := fieldsOfStruct(&Action{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = "a." + f.column
	}

	// The table is aliased and its columns qualified because ordering by
	// priority joins project, and both tables have a snooze_until.
	where, args := filter.clauses(s.now().UTC().Format(timeFormat))
	query := fmt.Sprintf("SELECT %s FROM action a", strings.Join(columns, ", "))
	if filter.Order == OrderPriority {
		query += " LEFT JOIN project p ON p.id = a.project_id"
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + actionOrder(filter.Order)

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

// actionOrder is the ORDER BY for a listing.
//
// Under OrderPriority: rank_pin first, because it exists to override whatever
// the system worked out; then the priority of the project the action
// advances, since an action inherits its urgency from what it is for; then
// creation order. Anything without a priority sorts after everything with
// one — unstated is not the same as low, but it has to go somewhere, and
// behind the stated ones is the reading that does no harm.
func actionOrder(order string) string {
	if order != OrderPriority {
		return "a.n"
	}
	return "a.rank_pin IS NULL, a.rank_pin, p.priority IS NULL, p.priority, a.n"
}

func (f ActionFilter) clauses(now string) ([]string, []any) {
	var where []string
	var args []any

	if f.State != "" {
		where = append(where, "a.state = ?")
		args = append(args, f.State)
	}
	if f.Verb != "" {
		where = append(where, "a.verb = ?")
		args = append(args, f.Verb)
	}
	if f.Project != "" {
		where = append(where, "a.project_id = ?")
		args = append(args, f.Project)
	}
	if f.Open {
		where = append(where, "a.closed_at IS NULL")
	}
	if f.Unblocked {
		where = append(where, "a.closed_at IS NULL", "a.state = ?", "a.hidden_behind IS NULL")
		args = append(args, ActionReady)
	}
	if f.Expired {
		where = append(where, "a.state = ? AND a.snooze_until IS NOT NULL AND a.snooze_until < ?")
		args = append(args, ActionSnoozed, now)
	}
	return where, args
}
