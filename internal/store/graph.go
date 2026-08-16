package store

import (
	"context"
	"fmt"
)

// Edge is one recorded dependency, from the thing that must happen first to
// the thing waiting on it.
//
// Identifiers only. What each end *is* — its status, its priority, whether it
// is closed — is already loaded by anything drawing a graph, and a second copy
// of it here would be a second answer to the same question.
type Edge struct {
	// From is the blocker: what has to finish.
	From string
	// To is what is waiting on it.
	To string
}

// ProjectBlockEdges is every project-to-project blocking edge.
//
// Every one, closed ends included, because who may draw what is the caller's
// question: a graph answering "what now" hides a satisfied blocker, and one
// explaining how the work got here shows it. Filtering in SQL would settle
// that here, where there is least to go on.
//
// Edges are never deleted, only made irrelevant by their blocker closing, so
// this is also the history — see UnblockProject, the one exception.
func (s *Store) ProjectBlockEdges(ctx context.Context) ([]Edge, error) {
	return edges(ctx, s, "project_blocks", `
		SELECT blocker_id, blocked_id FROM project_blocks
		 ORDER BY created_at, blocker_id, blocked_id`)
}

// ActionBlockEdges is every action-to-action blocking edge.
func (s *Store) ActionBlockEdges(ctx context.Context) ([]Edge, error) {
	return edges(ctx, s, "action_blocks", `
		SELECT blocker_id, blocked_id FROM action_blocks
		 ORDER BY created_at, blocker_id, blocked_id`)
}

// HiddenBehindEdges is every action folded out of the queue behind another.
//
// A different relationship from blocking and worth keeping separate: a blocked
// action is waiting, where a hidden one is being deliberately not shown yet.
// Both stop it being actionable, and only one of them is a dependency.
func (s *Store) HiddenBehindEdges(ctx context.Context) ([]Edge, error) {
	return edges(ctx, s, "hidden_behind", `
		SELECT hidden_behind, id FROM action
		 WHERE hidden_behind IS NOT NULL
		 ORDER BY n`)
}

// StackedOnEdges is every tracked pull request based on another.
//
// This is the dependency scottlaird/roz#88 was written believing did not
// exist. stacked_on was added by 0002 and set by nothing until head_ref
// arrived in 0030 gave ResolveStacking something to match against, so the
// column is populated now and a diagram can draw a stack.
func (s *Store) StackedOnEdges(ctx context.Context) ([]Edge, error) {
	return edges(ctx, s, "stacked_on", `
		SELECT stacked_on, id FROM pr
		 WHERE stacked_on IS NOT NULL
		 ORDER BY repo, number`)
}

func edges(ctx context.Context, s *Store, what, query string) ([]Edge, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading the %s edges: %w", what, err)
	}
	defer rows.Close()

	var found []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.To); err != nil {
			return nil, fmt.Errorf("reading a %s edge: %w", what, err)
		}
		found = append(found, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the %s edges: %w", what, err)
	}
	return found, nil
}
