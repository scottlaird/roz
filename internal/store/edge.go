package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Event kinds for the edges. They are written by hand rather than diffed,
// which is the exception to the rule and the reason for it: an edge lives in
// its own table, so there is no column on the action for diffing to catch.
//
// There is no unblocked event to match. Unblocking is a change to
// action.state, which the diff already logs; a second event saying the same
// thing would only be a second thing to keep true.
const (
	eventBlocked  = "blocked"
	eventLinked   = "linked"
	eventUnlinked = "unlinked"
)

// How a pull request relates to an action.
//
// An action has at most one subject, enforced by a partial unique index. That
// is what makes "one item covering two unrelated pull requests"
// unrepresentable rather than merely discouraged.
const (
	RoleSubject = "subject"
	RoleContext = "context"
)

// AddBlocker records that blocker must be closed before blocked can proceed.
//
// The blocked action moves to blocked if it was ready. Both writes share the
// transaction's correlation id, so the log reads as one act.
//
// This is not hidden_behind. Blocked-by is a fact about ordering, and a
// blocked action still appears in the queue with its blockers named;
// hidden_behind is the judgement that there is nothing to do about it at all.
func (t *Tx) AddBlocker(ctx context.Context, blocker, blocked *Action) error {
	if blocker.ID == blocked.ID {
		return fmt.Errorf("%s cannot block itself", blocked.ID)
	}
	cycle, err := t.blocks(ctx, blocked.ID, blocker.ID)
	if err != nil {
		return err
	}
	if cycle {
		return fmt.Errorf("%s already blocks %s, directly or through others; "+
			"the pair would never unblock", blocked.ID, blocker.ID)
	}

	_, err = t.tx.ExecContext(ctx,
		"INSERT INTO action_blocks (blocker_id, blocked_id, created_at) VALUES (?, ?, ?)",
		blocker.ID, blocked.ID, t.at)
	if err != nil {
		return fmt.Errorf("blocking %s on %s: %w", blocked.ID, blocker.ID, err)
	}

	err = t.emit(ctx, blocked, event{
		kind:     eventBlocked,
		newValue: blocker.ID,
		note:     blocker.Title,
	})
	if err != nil {
		return err
	}
	return t.applyBlockedState(ctx, blocked)
}

// applyBlockedState moves an action between ready and blocked to match its
// open blockers.
//
// It is the only place that decides which of the two an action is in, so
// adding an edge and closing a blocker cannot disagree. Snoozed, done and
// dropped are left alone: a snooze is a decision about time and outranks the
// graph, and a closed action's state is history.
func (t *Tx) applyBlockedState(ctx context.Context, a *Action) error {
	if a.State != ActionReady && a.State != ActionBlocked {
		return nil
	}
	blockers, err := t.OpenBlockers(ctx, a.ID)
	if err != nil {
		return err
	}

	want := ActionReady
	if len(blockers) > 0 {
		want = ActionBlocked
	}
	if a.State == want {
		return nil
	}

	after := a.Clone()
	after.State = want
	if _, err := t.Update(ctx, a, after); err != nil {
		return err
	}
	*a = *after
	return nil
}

// blocks reports whether from blocks to, directly or through other actions.
//
// Closed blockers are followed as well as open ones. A cycle through a closed
// action is still a cycle, and reopening is not a thing this schema does, but
// the edge would outlive the reason it looked harmless.
func (t *Tx) blocks(ctx context.Context, from, to string) (bool, error) {
	const query = `
		WITH RECURSIVE reachable(id) AS (
		  SELECT blocked_id FROM action_blocks WHERE blocker_id = ?
		  UNION
		  SELECT b.blocked_id FROM action_blocks b JOIN reachable r ON b.blocker_id = r.id
		)
		SELECT 1 FROM reachable WHERE id = ? LIMIT 1`

	var found int
	err := t.tx.QueryRowContext(ctx, query, from, to).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("looking for a cycle between %s and %s: %w", from, to, err)
	}
	return true, nil
}

// OpenBlockers returns the ids of the actions still holding this one up,
// in creation order. A closed blocker holds nothing up and is not returned.
func (t *Tx) OpenBlockers(ctx context.Context, id string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT b.blocker_id
		FROM action_blocks b JOIN action a ON a.id = b.blocker_id
		WHERE b.blocked_id = ? AND a.closed_at IS NULL
		ORDER BY b.created_at, b.blocker_id`, id)
	if err != nil {
		return nil, fmt.Errorf("reading the blockers of %s: %w", id, err)
	}
	defer rows.Close()

	var blockers []string
	for rows.Next() {
		var blocker string
		if err := rows.Scan(&blocker); err != nil {
			return nil, fmt.Errorf("reading the blockers of %s: %w", id, err)
		}
		blockers = append(blockers, blocker)
	}
	return blockers, rows.Err()
}

// Dependents returns the open actions this one blocks — what closing it
// would free.
func (t *Tx) Dependents(ctx context.Context, id string) ([]*Action, error) {
	return t.loadActions(ctx, `
		SELECT %s FROM action a
		WHERE a.closed_at IS NULL
		  AND a.id IN (SELECT blocked_id FROM action_blocks WHERE blocker_id = ?)
		ORDER BY a.n`, id)
}

// Hiding returns the open actions hidden behind this one.
func (t *Tx) Hiding(ctx context.Context, id string) ([]*Action, error) {
	return t.loadActions(ctx,
		"SELECT %s FROM action a WHERE a.closed_at IS NULL AND a.hidden_behind = ? ORDER BY a.n", id)
}

// loadActions runs a query whose single %s is the column list.
func (t *Tx) loadActions(ctx context.Context, query string, args ...any) ([]*Action, error) {
	fields, err := fieldsOfStruct(&Action{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = "a." + f.column
	}

	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(query, strings.Join(columns, ", ")), args...)
	if err != nil {
		return nil, fmt.Errorf("reading actions: %w", err)
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
			return nil, fmt.Errorf("reading actions: %w", err)
		}
		actions = append(actions, &a)
	}
	return actions, rows.Err()
}

// LinkPR attaches a pull request to an action.
//
// A second subject is refused by the partial unique index rather than by a
// check here, which is the point of having it: the constraint holds however
// the row is written.
func (t *Tx) LinkPR(ctx context.Context, a *Action, prID, role string) error {
	if role != RoleSubject && role != RoleContext {
		return fmt.Errorf("%q is not a role: use %s or %s", role, RoleSubject, RoleContext)
	}

	_, err := t.tx.ExecContext(ctx,
		"INSERT INTO action_pr (action_id, pr_id, role) VALUES (?, ?, ?)",
		a.ID, prID, role)
	if err != nil {
		return fmt.Errorf("linking %s to %s: %w", a.ID, prID, err)
	}

	return t.emit(ctx, a, event{
		kind:     eventLinked,
		field:    role,
		newValue: prID,
	})
}

// SubjectPR returns the pull request an action is about, and whether it has
// one. A context link is not a subject: it is background, and closing on it
// would close the wrong thing.
func (t *Tx) SubjectPR(ctx context.Context, id string) (string, bool, error) {
	var pr string
	err := t.tx.QueryRowContext(ctx,
		"SELECT pr_id FROM action_pr WHERE action_id = ? AND role = ?", id, RoleSubject).Scan(&pr)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading the subject of %s: %w", id, err)
	}
	return pr, true, nil
}

// LinkedActions returns the open actions whose subject is this pull request.
func (t *Tx) LinkedActions(ctx context.Context, prID string) ([]*Action, error) {
	return t.loadActions(ctx, `
		SELECT %s FROM action a
		WHERE a.closed_at IS NULL
		  AND a.id IN (SELECT action_id FROM action_pr WHERE pr_id = ? AND role = 'subject')
		ORDER BY a.n`, prID)
}
