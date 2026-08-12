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

	// ReadySince is when this last became something a person could act on:
	// un-hidden, or freed by its last blocker. NULL means since it was
	// created, which is the ordinary case.
	//
	// A deadline needs this because waiting_since answers a different
	// question. That one is when reviewers could first have seen the pull
	// request, which is right for wait_review and meaningless for a merge step
	// that did not exist yet — see the overdue query, where the two are taken
	// together.
	ReadySince sql.NullString `db:"ready_since"`

	ClosedAt     sql.NullString `db:"closed_at"`
	ClosedReason sql.NullString `db:"closed_reason"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`

	// LastVerifiedAt is when someone last checked this against reality and was
	// satisfied, which UpdatedAt cannot tell you: every write moves that one.
	// An action nobody has touched for a month is fine if it was verified on
	// Friday and alarming if it was not.
	LastVerifiedAt sql.NullString `db:"last_verified_at" kind:"observed"`

	// OkayToWaitUntil is when it stops being reasonable to still be waiting
	// on this one. NULL is the ordinary case and means the verb's WaitDays,
	// resolved when the deadline is checked rather than copied here — so
	// changing an allowance reaches the actions that never claimed an
	// exception to it.
	//
	// It is not a snooze, and the difference is the whole point: a snooze
	// hides something until a date, this reveals something after one.
	OkayToWaitUntil sql.NullString `db:"okay_to_wait_until"`
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
	// OrderStaleness is longest un-checked first: what nobody has looked at
	// in the longest time, rather than what nobody has changed. See
	// stalenessOrder.
	OrderStaleness = "staleness"
)

// stalenessOrder puts the least recently verified first, and anything never
// verified before all of it.
//
// That last part inverts the rule the other orderings follow, where unstated
// sorts last. It is not an exception so much as the same reasoning arriving
// somewhere else: a missing priority is an absence of information, while a
// missing verification is the information — nobody has ever checked this, so
// nothing is staler.
//
// prefix qualifies the column for a query that joins, and is empty otherwise.
func stalenessOrder(prefix string) string {
	return prefix + "last_verified_at IS NOT NULL, " +
		prefix + "last_verified_at, " + prefix + "n"
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

// expiredSnooze matches a snoozed action whose date has arrived or passed.
//
// The comparison is against a full timestamp, so a date-only snooze until
// today is already past it: "hide this until the 12th" stops hiding on the
// 12th rather than the 13th.
//
// One definition, shared by the Expired filter and by inPlay below. `--expired`
// is how you ask for these specifically and the queue now contains them, so two
// spellings of the same idea could put an item in one and not the other.
const expiredSnooze = "(a.state = ? AND a.snooze_until IS NOT NULL AND a.snooze_until < ?)"

// inPlay are the conditions an action meets to be worth listing at all: open,
// not folded out of the queue behind something else, and either ready or past
// the date it was snoozed until. Unblocked and Waiting are this plus opposite
// sides of the wait rank class.
//
// An expired snooze is in play because the alternative is losing it. A snooze
// says "hide this until a date"; after that date it went on hiding, and the
// only way back was to notice — which makes the mechanism for deferring work
// also a way to drop it.
//
// It stays snoozed rather than being woken. Waking would rewrite an authored
// column from a clock, which is a different kind of write from any this makes
// elsewhere, and it would throw away the reason it was deferred. The row is
// unchanged; the queue simply stops pretending the date has not arrived.
func inPlay(now string) ([]string, []any) {
	return []string{
			"a.closed_at IS NULL",
			"a.hidden_behind IS NULL",
			"(a.state = ? OR " + expiredSnooze + ")",
		},
		[]any{ActionReady, ActionSnoozed, now}
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
	switch order {
	case OrderPriority:
		return rankOrder()
	case OrderStaleness:
		return stalenessOrder("a.")
	default:
		return "a.n"
	}
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
		clauses, inPlayArgs := inPlay(now)
		where = append(where, clauses...)
		where = append(where, "a.verb NOT IN "+waitVerbs)
		args = append(args, inPlayArgs...)
		args = append(args, RankWait)
	}
	if f.Waiting {
		clauses, inPlayArgs := inPlay(now)
		where = append(where, clauses...)
		where = append(where, "a.verb IN "+waitVerbs)
		args = append(args, inPlayArgs...)
		args = append(args, RankWait)
	}
	if f.Expired {
		where = append(where, expiredSnooze)
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
