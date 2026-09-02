package store

import (
	"context"
	"database/sql"
	"fmt"
)

// BlockProject records that blocker must close before blocked can proceed.
//
// Deliberately the mirror of AddBlocker: same cycle check, same coupling of
// status to the edge, same event. One project waiting on another is the same
// relationship as one action waiting on another, and giving it a second
// spelling would be how the two drift.
//
// There is no project equivalent of hidden_behind. Folding something out of a
// queue is a judgement about a queue, and the project table is not one.
func (t *Tx) BlockProject(ctx context.Context, blocker, blocked *Project) error {
	if blocker.ID == blocked.ID {
		return fmt.Errorf("%s cannot block itself", blocked.ID)
	}
	cycle, err := t.projectBlocks(ctx, blocked.ID, blocker.ID)
	if err != nil {
		return err
	}
	if cycle {
		return fmt.Errorf("%s already blocks %s, directly or through others; "+
			"the pair would never unblock", blocked.ID, blocker.ID)
	}

	_, err = t.tx.ExecContext(ctx,
		"INSERT INTO project_blocks (blocker_id, blocked_id, created_at) VALUES (?, ?, ?)",
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
	return t.applyProjectBlockedState(ctx, blocked)
}

// UnblockProject removes the edge, whether or not the blocker ever closed.
//
// Sometimes the dependency was wrong rather than satisfied, and saying so
// should not require closing something that is not done.
func (t *Tx) UnblockProject(ctx context.Context, blocker, blocked *Project) error {
	result, err := t.tx.ExecContext(ctx,
		"DELETE FROM project_blocks WHERE blocker_id = ? AND blocked_id = ?",
		blocker.ID, blocked.ID)
	if err != nil {
		return fmt.Errorf("unblocking %s from %s: %w", blocked.ID, blocker.ID, err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if removed == 0 {
		return fmt.Errorf("%s does not block %s", blocker.ID, blocked.ID)
	}

	err = t.emit(ctx, blocked, event{
		kind:     eventUnblocked,
		oldValue: blocker.ID,
		note:     blocker.Title,
	})
	if err != nil {
		return err
	}
	return t.applyProjectBlockedState(ctx, blocked)
}

// applyProjectBlockedState moves a project between active and blocked to
// match its open blockers.
//
// The only place that decides which of the two a project is in, so adding an
// edge and closing a blocker cannot disagree — the same argument
// applyBlockedState makes for actions.
//
// Snoozed is left alone, and so is anything terminal. A snooze is a decision
// about time and outranks the graph; a closed project's status is history.
func (t *Tx) applyProjectBlockedState(ctx context.Context, p *Project) error {
	// Re-read rather than trust the caller's copy. A record loaded before
	// something else moved it reports the status it had then, and deciding
	// from that writes the old world back: a project snoozed since it was
	// loaded would be quietly woken, because the guard below would not see
	// the snooze. The schema catches that particular one — snooze_until and
	// status are coupled by a CHECK — which is how it was found.
	current, err := t.LoadProject(ctx, p.ID)
	if err != nil {
		return err
	}
	if current.Status != ProjectActive && current.Status != ProjectBlocked {
		*p = *current
		return nil
	}

	blockers, err := t.OpenProjectBlockers(ctx, current.ID)
	if err != nil {
		return err
	}

	want := ProjectActive
	if len(blockers) > 0 {
		want = ProjectBlocked
	}
	if current.Status == want {
		*p = *current
		return nil
	}

	after := current.Clone()
	after.Status = want
	if _, err := t.Update(ctx, current, after); err != nil {
		return err
	}
	*p = *after
	return nil
}

// projectBlocks reports whether from blocks to, directly or through others.
//
// Closed blockers are followed as well as open ones, for the reason the
// action version gives: a cycle through a closed project is still a cycle,
// and the edge would outlive the reason it looked harmless.
func (t *Tx) projectBlocks(ctx context.Context, from, to string) (bool, error) {
	const query = `
		WITH RECURSIVE reachable(id) AS (
		  SELECT blocked_id FROM project_blocks WHERE blocker_id = ?
		  UNION
		  SELECT b.blocked_id FROM project_blocks b JOIN reachable r ON b.blocker_id = r.id
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

// BlockedProjects returns the ids of every project currently blocked.
//
// Read as a set rather than asked per action, because a listing draws hundreds
// of rows and "is this one's project blocked" is the same question each time.
// The same status the queue filters on, so a marked row and a missing one
// cannot disagree.
func (s *Store) BlockedProjects(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM project WHERE status = ?", ProjectBlocked)
	if err != nil {
		return nil, fmt.Errorf("reading the blocked projects: %w", err)
	}
	defer rows.Close()

	blocked := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("reading a blocked project: %w", err)
		}
		blocked[id] = true
	}
	return blocked, rows.Err()
}

// OpenProjectBlockers returns the ids of the projects still holding this one
// up. A closed project holds nothing up and is not returned.
func (t *Tx) OpenProjectBlockers(ctx context.Context, id string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT b.blocker_id
		FROM project_blocks b JOIN project p ON p.id = b.blocker_id
		WHERE b.blocked_id = ? AND p.status NOT IN (?, ?, ?)
		ORDER BY b.created_at, b.blocker_id`,
		id, ProjectDone, ProjectRetired, ProjectSuperseded)
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

// BlockedProjects returns the open projects this one blocks — what closing it
// would free.
func (t *Tx) BlockedProjects(ctx context.Context, id string) ([]*Project, error) {
	blocked, err := t.loadProjects(ctx, `
		SELECT %s FROM project p
		WHERE p.status NOT IN (?, ?, ?)
		  AND p.id IN (SELECT blocked_id FROM project_blocks WHERE blocker_id = ?)
		ORDER BY p.n`, ProjectDone, ProjectRetired, ProjectSuperseded, id)
	if err != nil {
		return nil, fmt.Errorf("reading what %s blocks: %w", id, err)
	}
	return blocked, nil
}

// freeBlockedProjects re-evaluates everything waiting on a project that has
// just closed, and reports what became active.
//
// This is the half that did not exist: closing an action ran a cascade,
// closing a project did not, so a project blocked on another stayed blocked
// for ever unless somebody remembered.
func (t *Tx) freeBlockedProjects(ctx context.Context, id string) ([]*Project, error) {
	waiting, err := t.BlockedProjects(ctx, id)
	if err != nil {
		return nil, err
	}

	var freed []*Project
	for _, p := range waiting {
		was := p.Status
		if err := t.applyProjectBlockedState(ctx, p); err != nil {
			return nil, err
		}
		if p.Status != was {
			freed = append(freed, p)
		}
	}
	return freed, nil
}
