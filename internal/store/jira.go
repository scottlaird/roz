package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// JiraIssue is one Jira issue, as last observed.
//
// It is an entity rather than columns on a project because one piece of work
// legitimately maps to more than one issue — "allow scaling up" and "allow
// scaling down" being the case that found this — and a column holds one key.
//
// Everything but the key is observed: the key is a decision someone made, the
// rest is what Jira says. An issue may exist with nothing linked to it, which
// is what lets an observation be recorded before any project claims it.
type JiraIssue struct {
	ID string `db:"id" kind:"identity"`

	Summary  string         `db:"summary" kind:"observed"`
	Status   sql.NullString `db:"status" kind:"observed"`
	Sprint   sql.NullString `db:"sprint" kind:"observed"`
	Assignee sql.NullString `db:"assignee" kind:"observed"`
	SyncedAt sql.NullString `db:"synced_at" kind:"observed"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`
}

func (i *JiraIssue) table() string       { return "jira_issue" }
func (i *JiraIssue) subjectType() string { return "jira_issue" }
func (i *JiraIssue) subjectID() string   { return i.ID }

func (i *JiraIssue) Clone() *JiraIssue {
	clone := *i
	return &clone
}

// LoadJiraIssue reads one issue. A missing issue is sql.ErrNoRows, so a caller
// can tell "never observed" from "observed and empty".
func (t *Tx) LoadJiraIssue(ctx context.Context, key string) (*JiraIssue, error) {
	issue := &JiraIssue{ID: key}
	if err := t.Load(ctx, issue, key); err != nil {
		return nil, err
	}
	return issue, nil
}

// JiraObservation is what a Jira sync reports about one issue.
//
// Every field but the key is nullable, and the distinction is load-bearing:
// an invalid value means Jira said nothing about that field, and a valid
// empty one means it said the field is empty. Unassigning an issue is a fact;
// not mentioning the assignee is not.
type JiraObservation struct {
	Key      string
	Summary  sql.NullString
	Status   sql.NullString
	Sprint   sql.NullString
	Assignee sql.NullString

	// SyncedAt is when Jira was read. Empty means the transaction's time.
	SyncedAt string
}

// JiraResult is what a set of observations did.
type JiraResult struct {
	Applied []JiraApplied
}

// JiraApplied is one issue's outcome, and which projects reference it.
//
// Created says the issue had not been seen before. Nothing is unmatched any
// more: an issue is a record in its own right, so an observation about one
// nothing references is stored rather than discarded. Projects is reported so
// a caller can still say what the observation touched.
type JiraApplied struct {
	Key      string
	Created  bool
	Changes  []Change
	Projects []string
}

// ObserveJira applies Jira observations, creating issues it has not seen.
//
// It is keyed on the issue rather than on a project because that is what an
// integration would have: a Jira issue does not know it is TD106.
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
		applied, err := tx.applyJiraObservation(ctx, observation)
		if err != nil {
			return nil, err
		}
		result.Applied = append(result.Applied, *applied)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// applyJiraObservation writes one observation onto one issue, creating it if
// this is the first time it has been seen.
//
// synced_at moves whether or not anything else did, which is the point of
// having it: "nothing has changed since Tuesday" and "nobody has looked since
// Tuesday" are different, and only this column tells them apart.
func (t *Tx) applyJiraObservation(ctx context.Context, o JiraObservation) (*JiraApplied, error) {
	syncedAt := o.SyncedAt
	if syncedAt == "" {
		syncedAt = t.at
	}

	projects, err := t.ProjectsForJiraKey(ctx, o.Key)
	if err != nil {
		return nil, err
	}
	applied := &JiraApplied{Key: o.Key, Projects: projects}

	before, err := t.LoadJiraIssue(ctx, o.Key)
	if err != nil {
		if err != sql.ErrNoRows {
			return nil, err
		}
		issue := &JiraIssue{ID: o.Key}
		assign(issue, o, syncedAt)
		if err := t.Insert(ctx, issue); err != nil {
			return nil, err
		}
		applied.Created = true
		return applied, nil
	}

	after := before.Clone()
	assign(after, o, syncedAt)
	changes, err := t.Update(ctx, before, after)
	if err != nil {
		return nil, err
	}
	applied.Changes = changes
	return applied, nil
}

// assign copies the fields Jira actually mentioned. An invalid value is not a
// claim, so it leaves what was there — absence is not a fact.
func assign(issue *JiraIssue, o JiraObservation, syncedAt string) {
	if o.Summary.Valid {
		issue.Summary = o.Summary.String
	}
	if o.Status.Valid {
		issue.Status = o.Status
	}
	if o.Sprint.Valid {
		issue.Sprint = o.Sprint
	}
	if o.Assignee.Valid {
		issue.Assignee = o.Assignee
	}
	issue.SyncedAt = sql.NullString{String: syncedAt, Valid: true}
}

// LinkProjectJira records that a project tracks an issue. Idempotent: linking
// twice is not an error, because a caller re-running an import should not have
// to know what it already did.
//
// The issue row is created if it is not there. A link to an issue nobody has
// synced is a legitimate state — the key is known before Jira is read.
func (t *Tx) LinkProjectJira(ctx context.Context, projectID, issueKey string) error {
	if _, err := t.LoadJiraIssue(ctx, issueKey); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
		if err := t.Insert(ctx, &JiraIssue{ID: issueKey}); err != nil {
			return err
		}
	}
	_, err := t.tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO project_jira (project_id, issue_id, created_at) VALUES (?, ?, ?)",
		projectID, issueKey, t.at)
	if err != nil {
		return fmt.Errorf("linking %s to %s: %w", projectID, issueKey, err)
	}
	return t.emit(ctx, &Project{ID: projectID}, event{
		kind:     eventLinked,
		field:    "jira",
		newValue: issueKey,
	})
}

// UnlinkProjectJira removes the link, leaving the issue itself alone. The
// issue may be referenced by another project, and is worth keeping either way:
// what Jira said is not invalidated by nobody tracking it.
func (t *Tx) UnlinkProjectJira(ctx context.Context, projectID, issueKey string) error {
	res, err := t.tx.ExecContext(ctx,
		"DELETE FROM project_jira WHERE project_id = ? AND issue_id = ?", projectID, issueKey)
	if err != nil {
		return fmt.Errorf("unlinking %s from %s: %w", projectID, issueKey, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s does not track %s", projectID, issueKey)
	}
	return t.emit(ctx, &Project{ID: projectID}, event{
		kind:     eventUnlinked,
		field:    "jira",
		oldValue: issueKey,
	})
}

// JiraKeysForProject returns the issues a project tracks, in key order so a
// listing is stable.
func (t *Tx) JiraKeysForProject(ctx context.Context, projectID string) ([]string, error) {
	return t.jiraStrings(ctx,
		"SELECT issue_id FROM project_jira WHERE project_id = ? ORDER BY issue_id", projectID)
}

// ProjectsForJiraKey returns the projects tracking an issue, in number order.
func (t *Tx) ProjectsForJiraKey(ctx context.Context, key string) ([]string, error) {
	return t.jiraStrings(ctx,
		"SELECT j.project_id FROM project_jira j JOIN project p ON p.id = j.project_id "+
			"WHERE j.issue_id = ? ORDER BY p.n", key)
}

func (t *Tx) jiraStrings(ctx context.Context, query string, arg string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("reading jira links for %s: %w", arg, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("reading jira links for %s: %w", arg, err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListJiraIssues returns every issue, in key order.
func (s *Store) ListJiraIssues(ctx context.Context) ([]*JiraIssue, error) {
	fields, err := fieldsOf(&JiraIssue{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	query := fmt.Sprintf("SELECT %s FROM jira_issue ORDER BY id", strings.Join(columns, ", "))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing jira issues: %w", err)
	}
	defer rows.Close()

	var issues []*JiraIssue
	for rows.Next() {
		var issue JiraIssue
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&issue)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing jira issues: %w", err)
		}
		issues = append(issues, &issue)
	}
	return issues, rows.Err()
}

// JiraByProject returns each project's issues, keyed by project id.
//
// One query rather than one per project: the status page reads this for every
// row it draws, and a per-row lookup is the shape that turns a page render
// into N round trips.
func (s *Store) JiraByProject(ctx context.Context) (map[string][]*JiraIssue, error) {
	fields, err := fieldsOf(&JiraIssue{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = "i." + f.column
	}

	query := fmt.Sprintf(
		"SELECT j.project_id, %s FROM project_jira j "+
			"JOIN jira_issue i ON i.id = j.issue_id ORDER BY j.project_id, i.id",
		strings.Join(columns, ", "))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading jira links: %w", err)
	}
	defer rows.Close()

	byProject := map[string][]*JiraIssue{}
	for rows.Next() {
		var projectID string
		var issue JiraIssue
		dest := make([]any, 0, len(fields)+1)
		dest = append(dest, &projectID)
		for _, f := range fields {
			dest = append(dest, f.pointerOf(&issue))
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading jira links: %w", err)
		}
		copied := issue
		byProject[projectID] = append(byProject[projectID], &copied)
	}
	return byProject, rows.Err()
}
