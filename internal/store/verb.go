package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// How a verb closes.
const (
	ClosesHuman     = "human"
	ClosesPredicate = "predicate"
)

// Rank classes. These drive both the sort and the render class, so adding a
// verb does not mean editing the ordering logic — and a class that was never
// styled is not expressible, which is a defect the hand-maintained page had.
const (
	RankClick   = "click"
	RankDecide  = "decide"
	RankSession = "session"
	RankWait    = "wait"
)

// ValidateRankClass refuses a class the schema would refuse, naming the
// alternatives rather than quoting a CHECK constraint.
//
// The set is closed for the reason the sketch gives: a class drives both the
// sort and the render, so one that was never styled is not expressible.
func ValidateRankClass(class string) error {
	for _, known := range RankClasses {
		if class == known {
			return nil
		}
	}
	return fmt.Errorf("rank class %q is not recognised: use %s",
		class, strings.Join(RankClasses, ", "))
}

// ActionVerb is one entry in the vocabulary.
//
// The table is data so the vocabulary can grow without a deploy. The limit is
// sharp: it stores a predicate's name, never its expression.
type ActionVerb struct {
	Verb         string         `db:"verb" kind:"identity"`
	Label        string         `db:"label"`
	Closes       string         `db:"closes"`
	PredicateKey sql.NullString `db:"predicate_key"`
	RankClass    string         `db:"rank_class"`
	RequiresPR   bool           `db:"requires_pr"`
	Active       bool           `db:"active"`
	Description  string         `db:"description"`

	// StartsPipeline says whether closing an action with this verb opens the
	// repository's pipeline. Having a subject pull request is not on its own
	// a reason to: investigating one ends when you know the answer.
	StartsPipeline bool `db:"starts_pipeline"`

	// WaitDays is how long waiting is reasonable before it is worth somebody
	// noticing. NULL means never: a verb describing your own work cannot be
	// overdue, only undone.
	//
	// Calendar days, not working days. At 1, a wait that starts on Friday is
	// overdue on Saturday — which is tolerable only because an overdue wait
	// becomes something that sits in the queue until Monday rather than
	// something that demands attention when it fires.
	WaitDays sql.NullInt64 `db:"wait_days"`

	// RequiresRef says the verb needs a ref wait to be able to close, the way
	// RequiresPR says it needs a pull request. Separate flags rather than one
	// "needs a subject", because the remedies differ — `action link-pr`
	// against `action wait-ref` — and an error naming the wrong one is its own
	// small bug.
	RequiresRef bool `db:"requires_ref"`

	// RequiresOwner says the verb needs an owner to be able to close: which
	// group's review this step waits for. A third flag for the same reason
	// there are two — the remedies differ, and a step's spec means something
	// different to each. A ref spec resolves to a release; an owner spec is
	// the group being waited on, and lands on the action's waiting_for.
	RequiresOwner bool `db:"requires_owner"`

	// RequiresIssue says the verb needs a tracker issue to be able to close.
	// The fourth of these for the reason there are three: the remedy is
	// `roz action wait-issue`, and an error naming any of the others sends
	// somebody the wrong way.
	RequiresIssue bool `db:"requires_issue"`
}

func (v *ActionVerb) table() string       { return "actionverb" }
func (v *ActionVerb) subjectType() string { return "actionverb" }
func (v *ActionVerb) subjectID() string   { return v.Verb }

// keyColumn: the vocabulary is keyed on the verb itself, not on a surrogate.
// Every other record uses id, which is why this is an optional interface.
func (v *ActionVerb) keyColumn() string { return "verb" }

func (v *ActionVerb) Clone() *ActionVerb {
	clone := *v
	return &clone
}

// Predicate returns the function this verb closes on, if it has one.
func (v *ActionVerb) Predicate() (Predicate, bool) {
	if !v.PredicateKey.Valid {
		return nil, false
	}
	return LookupPredicate(v.PredicateKey.String)
}

// ListVerbs returns the vocabulary, ordered by how it closes and then by
// name, so predicate verbs and human ones read as two groups.
//
// activeOnly drops retired verbs. They are never deleted — closed actions and
// log rows still reference them — so retired ones stay readable.
func (s *Store) ListVerbs(ctx context.Context, activeOnly bool, sort Sort) ([]*ActionVerb, error) {
	fields, err := fieldsOfStruct(&ActionVerb{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	query := fmt.Sprintf("SELECT %s FROM actionverb", strings.Join(columns, ", "))
	if activeOnly {
		query += " WHERE active = 1"
	}
	if by := sort.SQL(""); by != "" {
		query += " ORDER BY " + by
	} else {
		query += " ORDER BY closes, verb"
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing verbs: %w", err)
	}
	defer rows.Close()

	var verbs []*ActionVerb
	for rows.Next() {
		var v ActionVerb
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&v)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing verbs: %w", err)
		}
		verbs = append(verbs, &v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing verbs: %w", err)
	}
	return verbs, nil
}

// checkPredicates refuses a database whose vocabulary names a predicate this
// build does not have.
//
// This runs when the store is opened, which is the point of it: the sketch is
// explicit that a missing predicate should fail loudly at startup rather than
// silently at three in the morning, when an action quietly stops closing and
// nothing says why.
//
// Retired verbs are skipped. A verb withdrawn along with its function should
// not stop the tool from running.
func checkPredicates(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx,
		`SELECT verb, predicate_key FROM actionverb
		 WHERE active = 1 AND closes = ? AND predicate_key IS NOT NULL`, ClosesPredicate)
	if err != nil {
		return fmt.Errorf("checking the verb vocabulary: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var verb, key string
		if err := rows.Scan(&verb, &key); err != nil {
			return fmt.Errorf("checking the verb vocabulary: %w", err)
		}
		if _, ok := LookupPredicate(key); !ok {
			return &ErrUnregisteredPredicate{Verb: verb, Key: key}
		}
	}
	return rows.Err()
}
