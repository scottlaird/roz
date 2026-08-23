package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// A Relation is one thing an entity is connected to that is not a column of
// its own table.
//
// The declaration names the connection and the function that reads it. It
// deliberately does not describe the join: the queries stay hand-written and
// tested where they are, and generating them was never the point. What this
// buys is that a reader of the entity can see what it is connected to, and
// that the things which print those connections stop each deciding for
// themselves what an action is attached to.
//
// A connection that is already a column needs nothing here. `action.project_id`
// and `action.hidden_behind` are declared by their `db` tags like anything
// else; only what lives in another table is missing, which is why this list
// is short.
type Relation struct {
	// Name is the key in JSON and the label in a detail listing.
	Name string
	// Load reads the far end for one record. Returning nil means there is
	// nothing there, and the relation is left out entirely rather than
	// rendered empty.
	//
	// Nil where the relation exists only to be traversed: a filter can follow
	// a project to its actions, and `show` does not print them, because
	// `action list --project` already does and printing them twice was the
	// thing this file set out to stop.
	Load func(ctx context.Context, tx *Tx, id string) (any, error)

	// Join describes how to reach the far end, for a caller that has to build
	// a query rather than read one record.
	//
	// The original comment here said the join was deliberately not described,
	// because the queries were hand-written and generating them was never the
	// point. That held while the only consumer was `show`. A filter that says
	// "projects with a waiting action" cannot be hand-written, because the
	// predicate inside it is somebody's typing — so the connection has to be
	// data, and this is that data. Load stays: what `show` prints is still a
	// query somebody wrote and tested.
	Join *Join
}

// JoinKind says whether the far end is one record or many.
type JoinKind int

const (
	// ToMany reads as a list, and is what an existential quantifier walks.
	ToMany JoinKind = iota
	// ToOne reads as a record or nothing — a project's parent.
	ToOne
)

// Join is the shape of a connection, in enough detail to generate a
// correlated subquery over it.
//
// Deliberately not a general join language. It says which table, which column
// on each side, and optionally the junction between them, which covers every
// connection roz has. Anything needing more than that is a query somebody
// should write by hand, and Load is where it goes.
type Join struct {
	Kind JoinKind
	// Table is the far table.
	Table string
	// Near is the column on this side; almost always "id".
	Near string
	// Far is the column on the far side that Near is matched against — or,
	// where there is a junction, the far column the junction points at.
	Far string
	// Via is the junction table for a many-to-many, nil for a direct link.
	Via *Junction
	// Blank returns an empty record of the far entity, so a caller can read
	// its columns without knowing its type.
	Blank func() any
}

// Junction is the table in the middle of a many-to-many.
type Junction struct {
	Table string
	// Near is its column pointing back at this entity.
	Near string
	// Far is its column pointing at the far entity.
	Far string
	// Only narrows the junction to rows matching fixed values — action_pr
	// carries both the subject and the context pull requests, and they are
	// different relations over one table.
	Only map[string]any
}

// OnlyClauses renders a junction's fixed conditions, in a stable order so the
// SQL a filter produces does not depend on a map walk.
func (j *Junction) OnlyClauses(alias string) ([]string, []any) {
	if j == nil || len(j.Only) == 0 {
		return nil, nil
	}
	columns := make([]string, 0, len(j.Only))
	for column := range j.Only {
		columns = append(columns, column)
	}
	sort.Strings(columns)

	clauses := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		clauses = append(clauses, fmt.Sprintf("%s.%s = ?", alias, column))
		args = append(args, j.Only[column])
	}
	return clauses, args
}

// Joins reports the traversable connections of a record, keyed by name.
func Joins(r any) map[string]Join {
	related, ok := r.(Related)
	if !ok {
		return nil
	}
	joins := map[string]Join{}
	for _, rel := range related.relations() {
		if rel.Join != nil {
			joins[rel.Name] = *rel.Join
		}
	}
	return joins
}

// TableOf names the table a record lives in, which is what a correlated
// subquery has to qualify its outer references with.
func TableOf(r any) (string, error) {
	record, ok := r.(Record)
	if !ok {
		return "", fmt.Errorf("%T is not a record", r)
	}
	return record.table(), nil
}

// Related is a Record connected to something beyond its own columns.
//
// The method is unexported for the same reason Record's are: what an entity
// is attached to is part of its definition, not something to be extended from
// outside.
type Related interface {
	Record
	relations() []Relation
}

// Relations loads everything a record is connected to, keyed by name.
//
// It returns nil for a record that declares none, so a caller can treat "has
// no relations" and "has none loaded" the same way — which is what lets the
// JSON encoder take any record at all.
//
// One query per relation, which is right for showing one record and wrong for
// a list. The status page keeps its own batched loaders for exactly that
// reason; see PRsByAction and JiraByProject.
func (t *Tx) Relations(ctx context.Context, r Record) (map[string]any, error) {
	related, ok := r.(Related)
	if !ok {
		return nil, nil
	}

	var loaded map[string]any
	for _, rel := range related.relations() {
		if rel.Load == nil {
			// Traversable but not printable: see Relation.Load.
			continue
		}
		value, err := rel.Load(ctx, t, r.subjectID())
		if err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}
		if loaded == nil {
			loaded = map[string]any{}
		}
		loaded[rel.Name] = value
	}
	return loaded, nil
}

// MarshalRecord encodes a record and what it is connected to.
//
// The package-level MarshalRecord takes no transaction and so can only see
// columns. That is the right answer for a list, where a query per row per
// relation would be the wrong shape; it was the wrong answer for `show`,
// where the table printed an action's blockers and the JSON silently did not.
func (t *Tx) MarshalRecord(ctx context.Context, r Record) ([]byte, error) {
	loaded, err := t.Relations(ctx, r)
	if err != nil {
		return nil, err
	}
	return MarshalRecordWith(r, loaded)
}

// relations for an action: both directions of the blocking edge, and the
// pull requests it is about.
//
// `blocking` is the direction the cascade already reads when it decides what
// closing this frees, and the ranking counts. It was visible nowhere.
func (a *Action) relations() []Relation {
	return []Relation{
		{
			Name: "blocked_by", Load: blockedByIDs,
			Join: &Join{
				Kind: ToMany, Table: "action", Near: "id", Far: "id",
				Via:   &Junction{Table: "action_blocks", Near: "blocked_id", Far: "blocker_id"},
				Blank: func() any { return &Action{} },
			},
		},
		{
			Name: "blocking", Load: blockingIDs,
			Join: &Join{
				Kind: ToMany, Table: "action", Near: "id", Far: "id",
				Via:   &Junction{Table: "action_blocks", Near: "blocker_id", Far: "blocked_id"},
				Blank: func() any { return &Action{} },
			},
		},
		{
			// The pull request this action is about, which is the traversal
			// the queue side wants: "waits whose pull request has merged".
			Name: "subject_pr", Load: subjectPRID,
			Join: &Join{
				Kind: ToOne, Table: "pr", Near: "id", Far: "id",
				Via: &Junction{
					Table: "action_pr", Near: "action_id", Far: "pr_id",
					Only: map[string]any{"role": RoleSubject},
				},
				Blank: func() any { return &PR{} },
			},
		},
		{
			Name: "context_prs", Load: contextPRIDs,
			Join: &Join{
				Kind: ToMany, Table: "pr", Near: "id", Far: "id",
				Via: &Junction{
					Table: "action_pr", Near: "action_id", Far: "pr_id",
					Only: map[string]any{"role": RoleContext},
				},
				Blank: func() any { return &PR{} },
			},
		},
		{
			// The project it advances, which is a column here and a
			// traversal-only relation: `pr list --filter 'actions.exists(a,
			// a.project.priority == 1)'` is the question #200 is named after,
			// and without this there is nothing for the second hop to follow.
			// It prints nothing — project_id is already on the row — and
			// exists so a filter can reach the project's own columns.
			Name: "project",
			Join: &Join{
				Kind: ToOne, Table: "project", Near: "project_id", Far: "id",
				Blank: func() any { return &Project{} },
			},
		},
		{
			// The tracker issue this waits on, which is the other end of a
			// wait that can also end on a date. #264 asks for both ends to be
			// visible so it is clear which one is still live, and the snooze
			// half was already a column while this one was in a table nothing
			// printed.
			Name: "waits_for_issue", Load: waitedOnIssueID,
			Join: &Join{
				Kind: ToOne, Table: "tracker_issue", Near: "id", Far: "id",
				Via: &Junction{
					Table: "action_tracker_issue", Near: "action_id", Far: "issue_id",
				},
				Blank: func() any { return &TrackerIssue{} },
			},
		},
		{Name: "held_by", Load: heldByIDs},
	}
}

// waitedOnIssueID names the issue an action waits for, absent where it waits
// on none — which is every action that is not a wait_issue.
func waitedOnIssueID(ctx context.Context, tx *Tx, id string) (any, error) {
	issue, err := tx.IssueWaitedOnBy(ctx, id)
	if err != nil || issue == "" {
		return nil, err
	}
	return issue, nil
}

// relations for a project: the Jira issues it tracks.
//
// Its actions are the other direction of action.project_id, which is a column
// on the far side and reachable with `action list --project`. Repeating it
// here would put the same fact in two places.
func (p *Project) relations() []Relation {
	return []Relation{
		{Name: "blocked_by", Load: projectBlockedByIDs},
		{Name: "blocking", Load: projectBlockingIDs},
		{
			Name: "issues", Load: issueIDs,
			Join: &Join{
				Kind: ToMany, Table: "tracker_issue", Near: "id", Far: "id",
				Via:   &Junction{Table: "project_tracker_issue", Near: "project_id", Far: "issue_id"},
				Blank: func() any { return &TrackerIssue{} },
			},
		},
		// The three below are traversal-only: they print nothing, for the
		// reason the comment above gives, and exist so a filter can follow
		// them.
		{
			Name: "actions",
			Join: &Join{
				Kind: ToMany, Table: "action", Near: "id", Far: "project_id",
				Blank: func() any { return &Action{} },
			},
		},
		{
			Name: "parent",
			Join: &Join{
				Kind: ToOne, Table: "project", Near: "parent_id", Far: "id",
				Blank: func() any { return &Project{} },
			},
		},
		{
			Name: "children",
			Join: &Join{
				Kind: ToMany, Table: "project", Near: "id", Far: "parent_id",
				Blank: func() any { return &Project{} },
			},
		},
	}
}

// relations for a pull request: the actions about it.
//
// This is the direction that was hardest to get at — a tracked pull request
// gave no clue which piece of work it belonged to.
func (r *PR) relations() []Relation {
	return []Relation{
		{
			Name: "actions", Load: actionsAboutPR,
			Join: &Join{
				Kind: ToMany, Table: "action", Near: "id", Far: "id",
				Via:   &Junction{Table: "action_pr", Near: "pr_id", Far: "action_id"},
				Blank: func() any { return &Action{} },
			},
		},
		{
			// The issues it is against, which is the edge the weekly wrap-up
			// reads and the one that existed only in prose until 0038.
			Name: "issues", Load: prIssueIDs,
			Join: &Join{
				Kind: ToMany, Table: "tracker_issue", Near: "id", Far: "id",
				Via:   &Junction{Table: "pr_tracker_issue", Near: "pr_id", Far: "issue_id"},
				Blank: func() any { return &TrackerIssue{} },
			},
		},
		{Name: "checks", Load: checkStates},
	}
}

// relations for an issue: the pull requests against it.
//
// The direction "which pull requests moved this issue" is asked from the
// issue as often as from the pull request, and a junction read one way is not
// readable the other without saying so.
func (i *TrackerIssue) relations() []Relation {
	return []Relation{
		{
			Name: "prs", Load: issuePRIDs,
			Join: &Join{
				Kind: ToMany, Table: "pr", Near: "id", Far: "id",
				Via:   &Junction{Table: "pr_tracker_issue", Near: "issue_id", Far: "pr_id"},
				Blank: func() any { return &PR{} },
			},
		},
	}
}

func blockedByIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	blockers, err := tx.OpenBlockers(ctx, id)
	if err != nil || len(blockers) == 0 {
		return nil, err
	}
	return blockers, nil
}

// heldByIDs names what is holding this action's project up, which is why the
// action is out of the queue.
//
// The action itself carries nothing to explain that — deliberately, since a
// blocked project is derived at query time rather than written onto its
// actions — so `action show` would otherwise say ready about something the
// queue will not offer.
//
// Nil where the project has no open blockers, which includes a project whose
// status was set by hand. That one still leaves the queue, and `project show`
// is where the reason for it is.
func heldByIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	a, err := tx.LoadAction(ctx, id)
	if err != nil || !a.ProjectID.Valid || a.ProjectID.String == "" {
		return nil, err
	}
	project, err := tx.LoadProject(ctx, a.ProjectID.String)
	if err != nil || project.Status != ProjectBlocked {
		return nil, err
	}
	blockers, err := tx.OpenProjectBlockers(ctx, project.ID)
	if err != nil || len(blockers) == 0 {
		return nil, err
	}
	return blockers, nil
}

func blockingIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	dependents, err := tx.Dependents(ctx, id)
	if err != nil || len(dependents) == 0 {
		return nil, err
	}
	return actionIDs(dependents), nil
}

// The subject and the context links are two relations rather than one list
// with a role on each entry, because they are two different questions. The
// schema allows at most one subject and closing reads only that; a context
// link is background and is never asked anything. Declaring them separately
// puts that rule in the shape instead of in a comment, and both render as
// themselves rather than as a blob of JSON.
func subjectPRID(ctx context.Context, tx *Tx, id string) (any, error) {
	pr, ok, err := tx.SubjectPR(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	return pr, nil
}

func contextPRIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	links, err := tx.PRsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, link := range links {
		if link.Role == RoleContext {
			ids = append(ids, link.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, nil
}

func projectBlockedByIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	blockers, err := tx.OpenProjectBlockers(ctx, id)
	if err != nil || len(blockers) == 0 {
		return nil, err
	}
	return blockers, nil
}

func projectBlockingIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	blocked, err := tx.BlockedProjects(ctx, id)
	if err != nil || len(blocked) == 0 {
		return nil, err
	}
	ids := make([]string, len(blocked))
	for i, p := range blocked {
		ids[i] = p.ID
	}
	return ids, nil
}

func issueIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	ids, err := tx.IssueIDsForProject(ctx, id)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return ids, nil
}

func prIssueIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	ids, err := tx.IssueIDsForPR(ctx, id)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return ids, nil
}

func issuePRIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	ids, err := tx.PRIDsForIssue(ctx, id)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return ids, nil
}

func actionsAboutPR(ctx context.Context, tx *Tx, id string) (any, error) {
	actions, err := tx.LinkedActions(ctx, id)
	if err != nil || len(actions) == 0 {
		return nil, err
	}
	return actionIDs(actions), nil
}

// checkStates renders the checks as name to state, which is the shape the old
// blob had — the difference is where it lives and how it is logged, not how a
// reader wants to see it.
func checkStates(ctx context.Context, tx *Tx, id string) (any, error) {
	checks, err := tx.ChecksFor(ctx, id)
	if err != nil || len(checks) == 0 {
		return nil, err
	}
	byName := make(map[string]string, len(checks))
	for _, c := range checks {
		byName[c.Name] = c.State
	}
	return byName, nil
}

func actionIDs(actions []*Action) []string {
	ids := make([]string, len(actions))
	for i, a := range actions {
		ids[i] = a.ID
	}
	return ids
}

// ReadRelated reads the far side of a join for one record, as column maps.
//
// One query, generated from the same description the filter pushes down, so
// the two paths cannot drift: whatever `EXISTS` walks is what this returns.
//
// Column maps rather than typed records, because the caller is a filter that
// only wants to compare values, and returning `any` per entity would mean a
// type switch for every entity roz has.
func ReadRelated(ctx context.Context, tx *Tx, join Join, id string) ([]map[string]any, error) {
	columns, err := ColumnTypes(join.Blank())
	if err != nil {
		return nil, err
	}
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = "far." + c.Name
	}

	query := fmt.Sprintf("SELECT %s FROM %s far", strings.Join(names, ", "), join.Table)
	args := []any{id}
	if join.Via != nil {
		query += fmt.Sprintf(" JOIN %s j ON j.%s = far.%s WHERE j.%s = ?",
			join.Via.Table, join.Via.Far, join.Far, join.Via.Near)
		if clauses, only := join.Via.OnlyClauses("j"); len(clauses) > 0 {
			query += " AND " + strings.Join(clauses, " AND ")
			args = append(args, only...)
		}
	} else {
		query += fmt.Sprintf(" WHERE far.%s = ?", join.Far)
	}

	rows, err := tx.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the far side of a join on %s: %w", join.Table, err)
	}
	defer rows.Close()

	var found []map[string]any
	for rows.Next() {
		holders := make([]any, len(columns))
		for i := range holders {
			holders[i] = new(any)
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, fmt.Errorf("reading the far side of a join on %s: %w", join.Table, err)
		}
		row := make(map[string]any, len(columns))
		for i, c := range columns {
			if value := *(holders[i].(*any)); value != nil {
				row[c.Name] = value
			}
		}
		found = append(found, row)
	}
	return found, rows.Err()
}
