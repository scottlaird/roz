package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// JiraObservation is what a Jira sync reports about one issue.
//
// Every field but the key is nullable, and the distinction is load-bearing:
// an invalid value means Jira said nothing about that field, and a valid
// empty one means it said the field is empty. Unassigning an issue is a fact;
// not mentioning the assignee is not.
type JiraObservation struct {
	Key      string
	Status   sql.NullString
	Sprint   sql.NullString
	Assignee sql.NullString

	// SyncedAt is when Jira was read. Empty means the transaction's time.
	SyncedAt string
}

// JiraResult is what an observation did, per project.
type JiraResult struct {
	Applied []JiraApplied
	// Unmatched are keys no project claims. Not an error: the tool tracks a
	// subset of what Jira holds, and a key nobody has claimed is normal.
	Unmatched []string
}

// JiraApplied is one project's share of an observation.
type JiraApplied struct {
	ProjectID string
	JiraKey   string
	Changes   []Change
}

// ObserveJira applies Jira observations to whatever projects carry the keys.
//
// It is keyed on jira_key rather than on project id because that is what an
// integration would have: a Jira issue does not know it is TD106. More than
// one project may carry the same key, and each gets the observation — the
// schema does not make jira_key unique, and two projects tracking one epic is
// a reasonable thing to do.
//
// The actor must be a sync actor, since every column written here is
// observed. `todo project jira` passes sync:jira-manual, so the log never
// claims an integration reported something typed in by hand.
func (s *Store) ObserveJira(ctx context.Context, actor Actor, observations []JiraObservation) (*JiraResult, error) {
	if actor.writes() != Observed {
		return nil, fmt.Errorf("%s may not record Jira observations: they are observed fields", actor)
	}

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result := &JiraResult{}
	for _, observation := range observations {
		projects, err := tx.ProjectsByJiraKey(ctx, observation.Key)
		if err != nil {
			return nil, err
		}
		if len(projects) == 0 {
			result.Unmatched = append(result.Unmatched, observation.Key)
			continue
		}
		for _, p := range projects {
			changes, err := tx.applyJiraObservation(ctx, p, observation)
			if err != nil {
				return nil, err
			}
			result.Applied = append(result.Applied, JiraApplied{
				ProjectID: p.ID, JiraKey: observation.Key, Changes: changes,
			})
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// applyJiraObservation writes one observation onto one project.
//
// jira_synced_at moves whether or not anything else did, which is the point
// of having it: "nothing has changed since Tuesday" and "nobody has looked
// since Tuesday" are different, and only this column tells them apart.
func (t *Tx) applyJiraObservation(ctx context.Context, p *Project, o JiraObservation) ([]Change, error) {
	after := p.Clone()
	if o.Status.Valid {
		after.JiraStatus = o.Status
	}
	if o.Sprint.Valid {
		after.JiraSprint = o.Sprint
	}
	if o.Assignee.Valid {
		after.JiraAssignee = o.Assignee
	}

	syncedAt := o.SyncedAt
	if syncedAt == "" {
		syncedAt = t.at
	}
	after.JiraSyncedAt = sql.NullString{String: syncedAt, Valid: true}

	changes, err := t.Update(ctx, p, after)
	if err != nil {
		return nil, err
	}
	*p = *after
	return changes, nil
}

// ProjectsByJiraKey returns the projects carrying a Jira key, in number
// order.
func (t *Tx) ProjectsByJiraKey(ctx context.Context, key string) ([]*Project, error) {
	fields, err := fieldsOf(&Project{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	query := fmt.Sprintf("SELECT %s FROM project WHERE jira_key = ? ORDER BY n",
		strings.Join(columns, ", "))
	rows, err := t.tx.QueryContext(ctx, query, key)
	if err != nil {
		return nil, fmt.Errorf("reading projects for %s: %w", key, err)
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		var p Project
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&p)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading projects for %s: %w", key, err)
		}
		projects = append(projects, &p)
	}
	return projects, rows.Err()
}
