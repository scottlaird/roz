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

	// WaitingFor is which of those the wait is actually for, and is authored
	// because nothing else could supply it: CODEOWNERS puts several teams on a
	// pull request because of files it happens to touch, and which one's
	// approval unblocks the work is something the author knows and GitHub does
	// not.
	//
	// It does not contradict WaitingOn and does not replace it. Both are true,
	// and this is usually one of that list — but it may be a person rather
	// than a team, or somebody GitHub never listed at all, when the real
	// dependency is one individual's context rather than a formal owner.
	//
	// Expect it to go stale. A team approves, the wait moves to the next one,
	// and the pull request has not changed at all.
	WaitingFor sql.NullString `db:"waiting_for"`

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
	// Where is a WHERE fragment somebody else compiled — the SQL half of a
	// CEL filter. Opaque here: what it means is the filter package's business.
	//
	// The qualifier gave: a filter compiled with filter.WithBaseAlias(
	// ActionAlias) names the outer row the way this query does, so a
	// traversal correlates correctly rather than referring to a table the
	// query never mentions. See scottlaird/roz#201.
	Where string
	// WhereArgs are its parameters, in the order the fragment names them.
	WhereArgs []any
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
	// Sort orders by columns instead, when one was asked for. It wins over
	// Order, which cannot be combined with it — a ranking is not a key to
	// break a tie in, it is the whole ordering.
	Sort Sort
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
// openAction is an action that is neither closed nor folded out of sight
// behind another. liveAction is that plus ready: the precondition every sweep
// that asks "is this worth somebody's attention" shares — the queue, the
// overdue check, the unannounced check.
//
// Named because it drifted. #262 fixed the unannounced sweep on the wrong
// column, and #265 found the state test the overdue sweep had and it lacked;
// a week of exceptions about steps a chain had not reached came from one
// clause somebody forgot. With the fragment named, the next divergence has to
// be a deliberate edit to it rather than an omission nobody sees. liveAction
// takes one argument, ActionReady, in the position of its placeholder.
const (
	openAction = "a.closed_at IS NULL AND a.hidden_behind IS NULL"
	liveAction = openAction + " AND a.state = ?"
)

// One definition, shared by the Expired filter, by inPlay below and by
// WakeExpired. `--expired` is how you ask for these specifically, the queue
// contains them, and the sweep wakes them, so two spellings of the same idea
// could put an item in one and not the others.
const expiredSnooze = "(a.state = ? AND a.snooze_until IS NOT NULL AND a.snooze_until < ?)"

// snoozeExpired is expiredSnooze in Go, for a row already in hand.
//
// The same comparison, against the same kind of instant, kept next to the SQL
// so the two are read together. It compares a stored date against a full
// timestamp as text, which is what the SQL does too: a snooze until a bare
// date is past as soon as that date begins, because "2026-08-24" sorts before
// "2026-08-24T00:00:00.000Z". Comparing against the date alone once made an
// item snoozed until today unexpired here and expired in the query — visible
// in the queue with nothing marking it. TestSnoozeExpiredAgreesWithTheQuery
// holds the two together at that boundary.
//
// A method rather than a column the query returns because the instant is the
// store's clock, which every loader would have to be handed; a generated
// column cannot see a clock at all.
func snoozeExpired(state, snoozedState string, until sql.NullString, now string) bool {
	return state == snoozedState && until.Valid && until.String != "" && until.String < now
}

// SnoozeExpired reports whether the action is snoozed until a date that has
// arrived — the rows `--expired` lists and WakeExpired wakes — as of now.
func (a *Action) SnoozeExpired(now string) bool {
	return snoozeExpired(a.State, ActionSnoozed, a.SnoozeUntil, now)
}

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
// It is still snoozed here because nothing has woken it yet. WakeExpired is
// what ends a deferral: it runs every sync cycle, moves the row back to ready
// and clears the date and the reason. So a row this branch matches is one the
// sweep has not reached — the seconds before the next cycle, or for ever when
// nothing is syncing — and the queue shows it rather than losing it. The
// branch is the fallback, not the rule. It used to be the rule, and refused
// to wake; the reasons it gave are answered in WakeExpired's comment.
func inPlay(now string) ([]string, []any) {
	return []string{openAction, "(a.state = ? OR " + expiredSnooze + ")"},
		[]any{ActionReady, ActionSnoozed, now}
}

// waitVerbs is the set of verbs whose own description is that there is
// nothing to do but wait. Naming the rank class rather than the verbs means a
// waiting verb added later is classified without editing this.
const waitVerbs = "(SELECT verb FROM actionverb WHERE rank_class = ?)"

// notHeldByItsProject keeps out the actions on a project that is blocked.
//
// The queue answers "what do I do now", and a blocked project's actions are
// exactly the ones that cannot be done now. Before this the project listing
// said blocked and the action listing said go, about the same fact.
//
// Derived here rather than written onto the action, which is the important
// part. An action already has blocked_by and hidden_behind, each with its own
// release condition; a third writer of the same state would raise the question
// of which one releases it, and the wrong answer leaves an action stuck after
// its project unblocks. Read at query time there is nothing to release: the
// status is maintained by applyProjectBlockedState, so the moment a project's
// last blocker closes its actions are back, with nobody having remembered
// which ones they were.
//
// project.status rather than a count of open blockers, so that this and
// `project list` cannot disagree — that disagreement is the bug.
//
// rank_pin is the escape. Not every action on a blocked project is blocked by
// the same thing: the `decide` that would remove the blocker, or a `file` that
// closes out a stale ticket, are exactly the work that clears it, and a rule
// applied uniformly buries them. Pinning one says "I mean this one", which is
// what rank_pin has always meant — see rankOrder, where it beats everything.
const notHeldByItsProject = `(a.rank_pin IS NOT NULL OR NOT EXISTS (
		SELECT 1 FROM project p WHERE p.id = a.project_id AND p.status = ?))`

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
// ActionAlias is what the listing query calls the action table.
//
// Exported because a filter compiled elsewhere has to agree: every correlated
// subquery a traversal generates points back at the outer row by name, and the
// name is this. See filter.WithBaseAlias.
const ActionAlias = "a"

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
	query += " ORDER BY " + actionOrder(filter)

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

// actionOrder is the ORDER BY for a listing: the columns asked for, else
// creation order or the ranking.
func actionOrder(filter ActionFilter) string {
	if sql := filter.Sort.SQL("a."); sql != "" {
		return sql
	}
	switch filter.Order {
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
		where = append(where, "a.verb NOT IN "+waitVerbs, notHeldByItsProject)
		args = append(args, inPlayArgs...)
		args = append(args, RankWait, ProjectBlocked)
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
	if f.Where != "" {
		where = append(where, "("+f.Where+")")
		args = append(args, f.WhereArgs...)
	}
	return where, args
}
