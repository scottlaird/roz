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
	Why string `db:"why" format:"markdown"`

	// HiddenBehind is not the blocked-by edge. Blocked-by is a fact about
	// ordering; hidden-behind is the judgement that there is nothing to do
	// about this one except clear the other, so it folds out of the queue.
	HiddenBehind sql.NullString `db:"hidden_behind"`

	SnoozeUntil  sql.NullString `db:"snooze_until"`
	SnoozeReason string         `db:"snooze_reason" format:"markdown"`

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
// arbitrary. OrderPriority is the sketch's ranking: the priority of what an
// action advances, then the verb's rank class, then how much finishing it
// frees, then effort — with rank_pin over all of it. See rankOrder.
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
	// Unblocked keeps what could be worked on right now: open, ready, not
	// folded out of the queue behind something else, and not waiting on
	// somebody. It is the queue.
	//
	// It reads state rather than counting blockers, because state is
	// recomputed from the open blockers wherever an edge or a closure moves
	// it — two ways of answering the same question would be one too many.
	//
	// Waiting is excluded by rank class rather than by naming wait_review, so
	// a wait verb added later is excluded without editing this. The queue
	// answers "what do I do now", and a verb whose own description is nothing
	// to do but wait is not an answer — use --open to see those.
	Unblocked bool
	// Order is how the results come back. Empty is creation order.
	Order string
	// Waiting keeps what the queue leaves out because it is waiting on
	// somebody: ready, unhidden, and a verb whose rank class says so. It is
	// the complement of Unblocked over the same set, which is why the two are
	// built from the same conditions — a queue and the things it deliberately
	// omits should not be able to disagree about what is in play.
	Waiting bool
	// Expired keeps snoozed actions whose date has passed — the query the
	// sketch calls the highest value in the system, because a snooze nobody
	// is watching is how work goes quiet.
	Expired bool
	// Stale keeps actions that claim to be finished while the pull request
	// they are about is still open. Nothing else notices that: closing is a
	// judgement and GitHub is a fact, and this is where the two disagree.
	Stale bool
}

// inPlay are the conditions an action meets to be worth listing at all: open,
// ready, and not folded out of the queue behind something else. Unblocked and
// Waiting are this plus opposite sides of the wait rank class.
func inPlay() []string {
	return []string{"a.closed_at IS NULL", "a.state = ?", "a.hidden_behind IS NULL"}
}

// waitVerbs is the set of verbs whose own description is that there is
// nothing to do but wait. Naming the rank class rather than the verbs means a
// waiting verb added later is classified without editing this.
const waitVerbs = "(SELECT verb FROM actionverb WHERE rank_class = ?)"

// staleSubjects matches actions whose subject pull request is still open.
//
// A pull request nobody has synced has no state, and is not matched: absence
// is not a fact, so an unsynced pull request is not evidence of anything.
const staleSubjects = `a.id IN (
		SELECT link.action_id FROM action_pr link JOIN pr ON pr.id = link.pr_id
		WHERE link.role = ? AND pr.state = ?)`

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

	// The table is aliased and its columns qualified because the ranking
	// joins project, and both tables have a snooze_until.
	where, args := filter.clauses(s.now().UTC().Format(timeFormat))

	var query string
	if filter.Order == OrderPriority {
		query = unblocksCTE
	}
	query += fmt.Sprintf("SELECT %s FROM action a", strings.Join(columns, ", "))
	if filter.Order == OrderPriority {
		query += rankJoins
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

// actionOrder is the ORDER BY for a listing: creation order, or the ranking.
func actionOrder(order string) string {
	if order != OrderPriority {
		return "a.n"
	}
	return rankOrder()
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
		where = append(where, inPlay()...)
		where = append(where, "a.verb NOT IN "+waitVerbs)
		args = append(args, ActionReady, RankWait)
	}
	if f.Waiting {
		where = append(where, inPlay()...)
		where = append(where, "a.verb IN "+waitVerbs)
		args = append(args, ActionReady, RankWait)
	}
	if f.Expired {
		where = append(where, "a.state = ? AND a.snooze_until IS NOT NULL AND a.snooze_until < ?")
		args = append(args, ActionSnoozed, now)
	}
	if f.Stale {
		// Only completion claims anything about the work. An abandoned action
		// with an open pull request is not a contradiction: it is someone
		// deciding not to finish.
		where = append(where, "a.closed_reason = ?", staleSubjects)
		args = append(args, ClosedCompleted, RoleSubject, PRStateOpen)
	}
	return where, args
}
