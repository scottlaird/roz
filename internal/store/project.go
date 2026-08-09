package store

import (
	"context"
	"database/sql"
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

// NewProject allocates an identifier and returns an unsaved Project.
//
// The identifier is consumed whether or not the caller goes on to insert it.
// CreatedAt is left to Insert.
func (s *Store) NewProject(ctx context.Context, title string) (*Project, error) {
	ident, err := s.Allocate(ctx, EntityProject)
	if err != nil {
		return nil, err
	}
	return &Project{
		ID:         ident.ID,
		Kind:       ident.Kind,
		N:          ident.N,
		Title:      title,
		Status:     ProjectActive,
		DesignRefs: "[]",
	}, nil
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
