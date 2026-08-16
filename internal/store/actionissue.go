package store

import (
	"context"
	"database/sql"
	"fmt"
)

// WaitOnIssue records the issue an action is waiting for, creating the issue
// row if nothing has recorded it yet.
//
// Creating the row is not a convenience. IssuesToPoll drives the poll from
// tracker_issue, so an issue nothing has recorded is never read, and a wait on
// it would be false for ever — the same trap requires_pr exists to prevent.
// Linking is therefore what puts the issue into the poll.
//
// Replaces rather than accumulates. One action waits on one issue: "this is
// blocked until that closes" is a single fact, and waiting on two issues is
// two waits, which the blocking graph says better because it can say which
// arrived first.
func (t *Tx) WaitOnIssue(ctx context.Context, actionID, tracker, key string) error {
	if err := ValidateTracker(tracker); err != nil {
		return err
	}
	id := IssueID(tracker, key)
	if _, err := t.LoadTrackerIssue(ctx, id); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
		if err := t.Insert(ctx, NewTrackerIssue(tracker, key)); err != nil {
			return err
		}
	}

	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO action_tracker_issue (action_id, issue_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT(action_id) DO UPDATE SET issue_id = excluded.issue_id`,
		actionID, id, t.at); err != nil {
		return fmt.Errorf("waiting %s on %s: %w", actionID, id, err)
	}
	return t.emit(ctx, &Action{ID: actionID}, event{
		kind: eventLinked, field: "issue", newValue: id,
	})
}

// IssueWaitedOnBy returns the issue an action waits for, empty where it waits
// on none.
func (t *Tx) IssueWaitedOnBy(ctx context.Context, actionID string) (string, error) {
	var id string
	err := t.tx.QueryRowContext(ctx,
		"SELECT issue_id FROM action_tracker_issue WHERE action_id = ?", actionID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading what %s waits on: %w", actionID, err)
	}
	return id, nil
}
