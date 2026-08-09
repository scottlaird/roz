package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Project statuses.
const (
	ProjectActive     = "active"
	ProjectBlocked    = "blocked"
	ProjectSnoozed    = "snoozed"
	ProjectDone       = "done"
	ProjectRetired    = "retired"
	ProjectSuperseded = "superseded"
)

// Project is a body of work — "split the Walker nodepool out of walker-core".
// Long-lived, often blocked, and at any moment most projects have no open
// action because there is nothing to do about them yet.
//
// The kind tags are the schema's authored/observed split. Everything from
// JiraStatus down is written only by sync; the rest only by a human or an
// agent. Tx.Update enforces that, so the tags are the whole of the rule.
//
// Prose belongs in Summary and nowhere else. Design notes live in files
// pointed to by DesignRefs — the test being whether the note would still be
// true if the project were cancelled.
type Project struct {
	ID   string `db:"id" kind:"identity"`
	Kind string `db:"kind" kind:"identity"`
	N    int64  `db:"n" kind:"identity"`

	Title        string         `db:"title"`
	Summary      string         `db:"summary"`
	Status       string         `db:"status"`
	Priority     sql.NullInt64  `db:"priority"`
	Effort       sql.NullString `db:"effort"`
	SnoozeUntil  sql.NullString `db:"snooze_until"`
	SnoozeReason string         `db:"snooze_reason"`
	SupersededBy sql.NullString `db:"superseded_by"`
	DesignRefs   string         `db:"design_refs"` // JSON array of file paths
	JiraKey      sql.NullString `db:"jira_key"`

	JiraStatus     sql.NullString `db:"jira_status" kind:"observed"`
	JiraSprint     sql.NullString `db:"jira_sprint" kind:"observed"`
	JiraAssignee   sql.NullString `db:"jira_assignee" kind:"observed"`
	JiraSyncedAt   sql.NullString `db:"jira_synced_at" kind:"observed"`
	LastVerifiedAt sql.NullString `db:"last_verified_at" kind:"observed"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`
}

func (p *Project) table() string       { return "project" }
func (p *Project) subjectType() string { return "project" }
func (p *Project) subjectID() string   { return p.ID }

// NewProject returns an unsaved Project with the defaults a new one takes.
//
// It allocates nothing and touches no database. Fill in the authored fields,
// then call Store.AllocateProject — in that order, so that rejecting bad
// input costs no identifier.
func NewProject(title string) *Project {
	return &Project{
		Title:      title,
		Status:     ProjectActive,
		DesignRefs: "[]",
	}
}

// AllocateProject gives a project its identifier.
//
// The number is consumed as soon as this returns, whether or not the insert
// that follows succeeds. Call it last, once the record is otherwise ready.
func (s *Store) AllocateProject(ctx context.Context, p *Project) error {
	ident, err := s.Allocate(ctx, EntityProject)
	if err != nil {
		return err
	}
	p.ID, p.Kind, p.N = ident.ID, ident.Kind, ident.N
	return nil
}

// LoadProject reads a project by id, returning sql.ErrNoRows if there is
// none.
func (t *Tx) LoadProject(ctx context.Context, id string) (*Project, error) {
	var p Project
	if err := t.Load(ctx, &p, id); err != nil {
		return nil, err
	}
	return &p, nil
}

// Clone returns a copy to mutate, leaving the original as the before image
// for Tx.Update. Project holds no reference types, so a shallow copy is a
// complete one.
func (p *Project) Clone() *Project {
	clone := *p
	return &clone
}

// ProjectFilter narrows ListProjects. The zero value selects everything.
type ProjectFilter struct {
	// Status keeps only projects in one status.
	Status string
	// Expired keeps snoozed projects whose date has passed — the highest
	// value query in the system, per the sketch, because a snooze nobody is
	// watching is how work goes quiet.
	Expired bool
	// Orphaned keeps projects with no open action and no snooze: live work
	// that is on no surface anyone reads.
	Orphaned bool
}

// ListProjects returns projects matching the filter, ordered by number.
//
// Ordering is on n rather than id, which is the reason n exists: SL100 sorts
// before SL41 lexically.
func (s *Store) ListProjects(ctx context.Context, filter ProjectFilter) ([]*Project, error) {
	fields, err := fieldsOf(&Project{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	where, args := filter.clauses(s.now().UTC().Format(timeFormat))
	query := fmt.Sprintf("SELECT %s FROM project", strings.Join(columns, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY n"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		var p Project
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointer(&p)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing projects: %w", err)
		}
		projects = append(projects, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	return projects, nil
}

// clauses renders the filter as SQL. now is compared as text, which works
// because ISO-8601 sorts lexically; a bare date such as 2026-08-21 sorts
// before any timestamp on that day, so it reads as expired from midnight.
func (f ProjectFilter) clauses(now string) ([]string, []any) {
	var where []string
	var args []any

	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Expired {
		where = append(where, "status = ? AND snooze_until IS NOT NULL AND snooze_until < ?")
		args = append(args, ProjectSnoozed, now)
	}
	if f.Orphaned {
		where = append(where,
			"snooze_until IS NULL AND id NOT IN (SELECT project_id FROM action WHERE closed_at IS NULL AND project_id IS NOT NULL)")
	}
	return where, args
}
