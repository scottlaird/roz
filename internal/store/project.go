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
	Summary      string         `db:"summary" format:"markdown"`
	Status       string         `db:"status"`
	Priority     sql.NullInt64  `db:"priority"`
	Effort       sql.NullString `db:"effort"`
	SnoozeUntil  sql.NullString `db:"snooze_until"`
	SnoozeReason string         `db:"snooze_reason" format:"markdown"`
	SupersededBy sql.NullString `db:"superseded_by"`
	DesignRefs   string         `db:"design_refs" format:"json"` // JSON array of file paths

	// Jira lives in jira_issue, reached through project_jira. It was five
	// columns here until one piece of work turned out to need two issues,
	// which a column cannot hold.
	LastVerifiedAt sql.NullString `db:"last_verified_at" kind:"observed"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`

	// ParentID is the project this is part of, for reading rather than for
	// scheduling: it does not block, does not close, and nothing about ranking
	// consults it. Most projects have none.
	ParentID sql.NullString `db:"parent_id"`

	// contextOnly marks a project pulled into a listing to connect others
	// rather than because it matched. Not a column: it is a fact about this
	// listing, not about the project.
	contextOnly bool
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

// closedProjectStatuses are the statuses that mean a project is over.
//
// Stated as the closed set rather than the open one so that a status added
// later counts as live by default, which is the safer side of the question:
// something new showing up in a list of work is a nuisance, and something new
// silently missing from one is how work goes quiet.
//
// IsOpen and the orphaned query both read this, so they cannot disagree.
var closedProjectStatuses = []string{ProjectDone, ProjectRetired, ProjectSuperseded}

// IsOpen reports whether the project is still live work. Action has the same
// method for the same reason.
func (p *Project) IsOpen() bool {
	for _, closed := range closedProjectStatuses {
		if p.Status == closed {
			return false
		}
	}
	return true
}

// ProjectFilter narrows ListProjects. The zero value selects everything.
type ProjectFilter struct {
	// Status keeps only projects in one status.
	Status string
	// Expired keeps snoozed projects whose date has passed — the highest
	// value query in the system, per the sketch, because a snooze nobody is
	// watching is how work goes quiet.
	Expired bool
	// Open keeps everything not terminal — active, blocked or snoozed.
	//
	// The page reads this rather than filtering to active, because a blocked
	// project vanishing is how SL22 sat invisible for a session: blocked is
	// exactly where work goes quiet, so it is the last thing to hide.
	Open bool
	// Orphaned keeps live projects with no open action and no snooze: work
	// that is on no surface anyone reads.
	//
	// Live is load-bearing. A finished project trivially has no open action,
	// so without that condition every one of them matches and the result is a
	// list nobody scans — which is the whole use of the query.
	Orphaned bool
	// Where is a WHERE fragment somebody else compiled — the SQL half of a
	// CEL filter, including any correlated subquery it reaches through.
	Where string
	// WhereArgs are its parameters, in the order the fragment names them.
	WhereArgs []any
	// Order is how the results come back. Empty is creation order.
	Order string
	// Sort orders by columns instead, when one was asked for. It wins over
	// the ranking, which cannot be combined with it: a ranking is not a key
	// to break a tie in, it is the whole ordering.
	Sort Sort
}

// projectOrder is the ORDER BY for a listing. Under OrderPriority the
// unprioritised sort last: unstated is not the same as low, but it has to go
// somewhere, and behind the stated ones is the reading that does no harm.
func projectOrder(filter ProjectFilter) string {
	if sql := filter.Sort.SQL(""); sql != "" {
		return sql
	}
	switch filter.Order {
	case OrderPriority:
		return "priority IS NULL, priority, n"
	case OrderStaleness:
		return stalenessOrder("")
	default:
		return "n"
	}
}

// ListProjects returns projects matching the filter.
//
// Creation order by default, and ordering is on n rather than id, which is
// the reason n exists: ROZ100 sorts before ROZ41 lexically.
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
	query += " ORDER BY " + projectOrder(filter)

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
	if f.Open {
		placeholders := make([]string, len(closedProjectStatuses))
		for i, status := range closedProjectStatuses {
			placeholders[i] = "?"
			args = append(args, status)
		}
		where = append(where, fmt.Sprintf("status NOT IN (%s)", strings.Join(placeholders, ", ")))
	}
	if f.Expired {
		where = append(where, "status = ? AND snooze_until IS NOT NULL AND snooze_until < ?")
		args = append(args, ProjectSnoozed, now)
	}
	if f.Orphaned {
		placeholders := make([]string, len(closedProjectStatuses))
		for i, status := range closedProjectStatuses {
			placeholders[i] = "?"
			args = append(args, status)
		}
		where = append(where,
			fmt.Sprintf("status NOT IN (%s)", strings.Join(placeholders, ", ")),
			"snooze_until IS NULL AND id NOT IN (SELECT project_id FROM action WHERE closed_at IS NULL AND project_id IS NOT NULL)")
	}
	if f.Where != "" {
		where = append(where, "("+f.Where+")")
		args = append(args, f.WhereArgs...)
	}
	return where, args
}

// ErrParentCycle reports a parent that would make a project its own ancestor.
type ErrParentCycle struct {
	Child, Parent string
	// Through is the chain from the proposed parent back to the child, so the
	// error can say which link is the problem rather than only that there is
	// one.
	Through []string
}

func (e *ErrParentCycle) Error() string {
	if len(e.Through) <= 1 {
		return fmt.Sprintf("%s cannot be its own parent", e.Child)
	}
	return fmt.Sprintf("%s cannot be a parent of %s: %s is already under it, through %s",
		e.Parent, e.Child, e.Parent, strings.Join(e.Through, " → "))
}

// SetParent records which project this one is part of, refusing a cycle.
//
// Walked rather than constrained, because a cycle is a property of the chain
// and not of any one row: the schema can refuse a project that is its own
// parent, and nothing more. This walks up from the proposed parent, and a
// child found on the way is the cycle.
//
// The walk is bounded by the number of projects, so a chain already circular —
// which nothing here can write, but a hand-edited database could — terminates
// rather than spinning.
func (t *Tx) SetParent(ctx context.Context, child *Project, parent string) error {
	if err := t.CheckParent(ctx, child, parent); err != nil {
		return err
	}
	after := child.Clone()
	after.ParentID = sql.NullString{String: parent, Valid: parent != ""}
	_, err := t.Update(ctx, child, after)
	return err
}

// CheckParent reports whether a project could be made part of another.
//
// Separate from writing it, so a command setting several columns at once can
// validate this one and then write them together.
func (t *Tx) CheckParent(ctx context.Context, child *Project, parent string) error {
	if parent == "" {
		return nil
	}
	if parent == child.ID {
		return &ErrParentCycle{Child: child.ID, Parent: parent}
	}

	chain, err := t.ancestry(ctx, parent)
	if err != nil {
		return err
	}
	for i, id := range chain {
		if id == child.ID {
			return &ErrParentCycle{Child: child.ID, Parent: parent, Through: chain[:i+1]}
		}
	}
	return nil
}

// ancestry returns a project and everything above it, nearest first.
func (t *Tx) ancestry(ctx context.Context, id string) ([]string, error) {
	var chain []string
	seen := map[string]bool{}

	for id != "" && !seen[id] {
		seen[id] = true
		chain = append(chain, id)

		var parent sql.NullString
		err := t.tx.QueryRowContext(ctx,
			"SELECT parent_id FROM project WHERE id = ?", id).Scan(&parent)
		if err == sql.ErrNoRows {
			return chain, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading what %s is part of: %w", id, err)
		}
		id = parent.String
	}
	return chain, nil
}

// IsContext reports whether this project is in a listing only to connect the
// ones that were asked for — a closed parent above open work.
func (p *Project) IsContext() bool { return p.contextOnly }
