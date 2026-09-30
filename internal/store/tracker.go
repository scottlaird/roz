package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Trackers roz can hold an issue from.
//
// A closed set for the reason pr.tracked_because is one: consumers branch on
// it, and a tracker nothing can read is not useful. github is here before
// anything reads it so that the schema is not what blocks that work.
const (
	TrackerJira   = "jira"
	TrackerGitHub = "github"
)

// Trackers is the vocabulary, in the order a listing should show it.
var Trackers = []string{TrackerJira, TrackerGitHub}

// ValidateTracker refuses a tracker the schema would refuse, with an error
// that names the alternatives rather than quoting a CHECK constraint.
func ValidateTracker(tracker string) error {
	for _, known := range Trackers {
		if tracker == known {
			return nil
		}
	}
	return fmt.Errorf("tracker %q is not recognised: use %s",
		tracker, strings.Join(Trackers, " or "))
}

// IssueID composes the identifier, the way a pull request's is composed from
// its repository and number.
//
// Composed rather than trusting the keys not to collide. 'CDSS-1744' and
// 'owner/repo#123' do not collide today, but that is a property of two third
// parties rather than something this can rely on.
func IssueID(tracker, key string) string { return tracker + ":" + key }

// TrackerIssue is one issue in somebody else's tracker, as last observed.
//
// It is an entity rather than columns on a project because one piece of work
// legitimately maps to more than one issue — "allow scaling up" and "allow
// scaling down" being the case that found this — and a column holds one key.
//
// Everything but the identity is observed: which issue this is was a decision
// someone made, the rest is what the tracker says. An issue may exist with
// nothing linked to it, which is what lets an observation be recorded before
// any project claims it.
type TrackerIssue struct {
	ID string `db:"id" kind:"identity"`
	// Tracker and Key compose ID and never change. Stored rather than parsed
	// back out of it, because "issues from this tracker" is a real query and
	// a LIKE against a prefix is not how to ask it.
	Tracker string `db:"tracker" kind:"identity"`
	Key     string `db:"key" kind:"identity"`

	Summary string         `db:"summary" kind:"observed"`
	Status  sql.NullString `db:"status" kind:"observed"`
	// Iteration is Jira's sprint and GitHub's milestone: the same field under
	// two names, so it carries neither.
	Iteration sql.NullString `db:"iteration" kind:"observed"`
	Assignee  sql.NullString `db:"assignee" kind:"observed"`
	SyncedAt  sql.NullString `db:"synced_at" kind:"observed"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`

	// ClosedAt is when the tracker says it closed, for the reason
	// pr.merged_at exists: status says where an issue is now, and the log says
	// when roz noticed, neither of which answers "what closed last week".
	//
	// NULL means not closed — or, for a tracker nothing reads, that nobody has
	// said. Only GitHub supplies this on its own; a Jira issue has it only if
	// `issue observe --closed-at` was given one, so its absence is not
	// evidence the issue is open. Status is what answers that.
	ClosedAt sql.NullString `db:"closed_at" kind:"observed"`
}

func (i *TrackerIssue) table() string       { return "tracker_issue" }
func (i *TrackerIssue) subjectType() string { return "tracker_issue" }
func (i *TrackerIssue) subjectID() string   { return i.ID }

func (i *TrackerIssue) Clone() *TrackerIssue {
	clone := *i
	return &clone
}

// NewTrackerIssue builds an unobserved issue: the identity, and nothing said
// about it yet.
func NewTrackerIssue(tracker, key string) *TrackerIssue {
	return &TrackerIssue{ID: IssueID(tracker, key), Tracker: tracker, Key: key}
}

// LoadTrackerIssue reads one issue by its composed id. A missing issue is
// sql.ErrNoRows, so a caller can tell "never observed" from "observed and
// empty".
func (t *Tx) LoadTrackerIssue(ctx context.Context, id string) (*TrackerIssue, error) {
	issue := &TrackerIssue{ID: id}
	if err := t.Load(ctx, issue, id); err != nil {
		return nil, err
	}
	return issue, nil
}

// TrackerObservation is what a sync reports about one issue.
//
// Every field but the identity is nullable, and the distinction is
// load-bearing: an invalid value means the tracker said nothing about that
// field, and a valid empty one means it said the field is empty. Unassigning
// an issue is a fact; not mentioning the assignee is not.
type TrackerObservation struct {
	Tracker   string
	Key       string
	Summary   sql.NullString
	Status    sql.NullString
	Iteration sql.NullString
	Assignee  sql.NullString
	// ClosedAt is when the tracker says the issue closed. Invalid means the
	// tracker said nothing about it, which for an open issue and for a
	// tracker that does not report closure are the same silence.
	ClosedAt sql.NullString

	// SyncedAt is when the tracker was read. Empty means the transaction's time.
	SyncedAt string
}

// ID is the issue this observation is about.
func (o TrackerObservation) ID() string { return IssueID(o.Tracker, o.Key) }

// TrackerResult is what a set of observations did.
type TrackerResult struct {
	Applied []TrackerApplied
}

// TrackerApplied is one issue's outcome, and which projects reference it.
//
// Created says the issue had not been seen before. Nothing is unmatched any
// more: an issue is a record in its own right, so an observation about one
// nothing references is stored rather than discarded. Projects is reported so
// a caller can still say what the observation touched.
type TrackerApplied struct {
	Tracker  string
	Key      string
	Created  bool
	Changes  []Change
	Projects []string
}

// ID is the issue this outcome is about.
func (a TrackerApplied) ID() string { return IssueID(a.Tracker, a.Key) }

// ObserveTrackerIssues applies observations, creating issues it has not seen.
//
// It is keyed on the issue rather than on a project because that is what an
// integration would have: a Jira issue does not know it is ROZ106.
//
// The actor must be a sync actor, since every column written here is
// observed. `roz issue observe` passes sync:<tracker>-manual, so the log never
// claims an integration reported something typed in by hand.
func (s *Store) ObserveTrackerIssues(ctx context.Context, actor Actor, observations []TrackerObservation) (*TrackerResult, error) {
	if actor.writes() != Observed {
		return nil, fmt.Errorf("%s may not record tracker observations: they are observed fields", actor)
	}
	for _, o := range observations {
		if err := ValidateTracker(o.Tracker); err != nil {
			return nil, err
		}
	}

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result := &TrackerResult{}
	for _, observation := range observations {
		applied, err := tx.applyTrackerObservation(ctx, observation)
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

// applyTrackerObservation writes one observation onto one issue, creating it if
// this is the first time it has been seen.
//
// synced_at moves with the state rather than with the reading, which is the
// same trade pr.last_synced_at makes and for the same reason: a poll every
// fifteen seconds would otherwise write a row and log an event per issue per
// cycle, and bury every real transition under a heartbeat. So it means "when
// the stored state last changed".
//
// A caller that states a time is different, and keeps the older meaning: an
// import saying an issue was read on Tuesday is asserting a fact about when
// somebody looked, not reporting that nothing has happened since.
func (t *Tx) applyTrackerObservation(ctx context.Context, o TrackerObservation) (*TrackerApplied, error) {
	projects, err := t.ProjectsForIssue(ctx, o.ID())
	if err != nil {
		return nil, err
	}
	applied := &TrackerApplied{Tracker: o.Tracker, Key: o.Key, Projects: projects}

	before, err := t.LoadTrackerIssue(ctx, o.ID())
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		issue := NewTrackerIssue(o.Tracker, o.Key)
		assign(issue, o)
		issue.SyncedAt = sql.NullString{String: t.timeOf(o), Valid: true}
		if err := t.Insert(ctx, issue); err != nil {
			return nil, err
		}
		applied.Created = true
		return applied, nil
	}

	after := before.Clone()
	assign(after, o)

	// Whether the reading is worth recording depends on whether it found
	// anything, so the state has to be diffed before synced_at is touched —
	// stamping it first would make every observation look like a change.
	moved, err := diff(before, after)
	if err != nil {
		return nil, err
	}
	if len(moved) > 0 || o.SyncedAt != "" {
		after.SyncedAt = sql.NullString{String: t.timeOf(o), Valid: true}
	}

	changes, err := t.Update(ctx, before, after)
	if err != nil {
		return nil, err
	}
	applied.Changes = changes
	return applied, nil
}

// timeOf is when an observation says it was made, defaulting to the
// transaction's own time.
func (t *Tx) timeOf(o TrackerObservation) string {
	if o.SyncedAt != "" {
		return o.SyncedAt
	}
	return t.at
}

// assign copies the fields the tracker actually mentioned. An invalid value is
// not a claim, so it leaves what was there — absence is not a fact.
//
// synced_at is the caller's, since whether a reading counts as news is not
// something this can see.
func assign(issue *TrackerIssue, o TrackerObservation) {
	if o.Summary.Valid {
		issue.Summary = o.Summary.String
	}
	if o.Status.Valid {
		issue.Status = o.Status
	}
	if o.Iteration.Valid {
		issue.Iteration = o.Iteration
	}
	if o.Assignee.Valid {
		issue.Assignee = o.Assignee
	}
	// A closure is permanent, so an observation that says nothing about it
	// never has to unset one. That is the same rule the other fields follow
	// and it happens to be the right one here rather than merely uniform:
	// GitHub sends no closedAt for an open issue, and reopening is what the
	// status is for.
	if o.ClosedAt.Valid {
		issue.ClosedAt = o.ClosedAt
	}
}

// LinkProjectIssue records that a project tracks an issue. Idempotent: linking
// twice is not an error, because a caller re-running an import should not have
// to know what it already did.
//
// The issue row is created if it is not there. A link to an issue nobody has
// synced is a legitimate state — the key is known before the tracker is read.
func (t *Tx) LinkProjectIssue(ctx context.Context, projectID, tracker, key string) error {
	if err := ValidateTracker(tracker); err != nil {
		return err
	}
	id := IssueID(tracker, key)
	if _, err := t.LoadTrackerIssue(ctx, id); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := t.Insert(ctx, NewTrackerIssue(tracker, key)); err != nil {
			return err
		}
	}
	_, err := t.tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO project_tracker_issue (project_id, issue_id, created_at) VALUES (?, ?, ?)",
		projectID, id, t.at)
	if err != nil {
		return fmt.Errorf("linking %s to %s: %w", projectID, id, err)
	}
	return t.emit(ctx, &Project{ID: projectID}, event{
		kind:     eventLinked,
		field:    "issue",
		newValue: id,
	})
}

// UnlinkProjectIssue removes the link, leaving the issue itself alone. The
// issue may be referenced by another project, and is worth keeping either way:
// what the tracker said is not invalidated by nobody tracking it.
func (t *Tx) UnlinkProjectIssue(ctx context.Context, projectID, tracker, key string) error {
	id := IssueID(tracker, key)
	res, err := t.tx.ExecContext(ctx,
		"DELETE FROM project_tracker_issue WHERE project_id = ? AND issue_id = ?", projectID, id)
	if err != nil {
		return fmt.Errorf("unlinking %s from %s: %w", projectID, id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s does not track %s", projectID, id)
	}
	return t.emit(ctx, &Project{ID: projectID}, event{
		kind:     eventUnlinked,
		field:    "issue",
		oldValue: id,
	})
}

// IssueIDsForProject returns the issues a project tracks, in id order so a
// listing is stable.
func (t *Tx) IssueIDsForProject(ctx context.Context, projectID string) ([]string, error) {
	return t.issueStrings(ctx,
		"SELECT issue_id FROM project_tracker_issue WHERE project_id = ? ORDER BY issue_id", projectID)
}

// ProjectsForIssue returns the projects tracking an issue, in number order.
func (t *Tx) ProjectsForIssue(ctx context.Context, id string) ([]string, error) {
	return t.issueStrings(ctx,
		"SELECT j.project_id FROM project_tracker_issue j JOIN project p ON p.id = j.project_id "+
			"WHERE j.issue_id = ? ORDER BY p.n", id)
}

func (t *Tx) issueStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	// The first argument is the subject in every caller, and is what an error
	// should name.
	var subject any
	if len(args) > 0 {
		subject = args[0]
	}
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading issue links for %s: %w", subject, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("reading issue links for %s: %w", subject, err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// IssueFilter selects a subset of the issues that have been observed. The
// zero value selects everything.
type IssueFilter struct {
	// SQLWhere is the compiled half of a CEL filter. See #201.
	SQLWhere
	// Tracker keeps one tracker's issues.
	Tracker string
	// Closed keeps issues the tracker has said closed.
	//
	// Read from closed_at rather than from status, which is the tracker's own
	// vocabulary and unconstrained on purpose — 'Done', 'Closed', 'Resolved'
	// and 'Won't Fix' are four trackers' words for one idea, and matching them
	// would be roz deciding what somebody else's workflow means.
	//
	// The cost is that an issue closed on a tracker nothing reads does not
	// appear until somebody records when. That is the honest shape: roz has
	// not been told.
	Closed bool
	// Since keeps issues closed at or after a timestamp, and selects closed
	// ones for the same reason PRFilter.Since selects merged ones.
	Since string
	// Sort orders by columns instead, when one was asked for. It wins over
	// the ranking, which cannot be combined with it: a ranking is not a key
	// to break a tie in, it is the whole ordering.
	Sort Sort
}

// aboutClosures reports whether the filter is asking what finished, which is
// what decides the order.
func (f IssueFilter) aboutClosures() bool { return f.Closed || f.Since != "" }

func (f IssueFilter) clauses() ([]string, []any) {
	var where []string
	var args []any

	if f.Tracker != "" {
		where = append(where, "tracker = ?")
		args = append(args, f.Tracker)
	}
	if f.Closed {
		where = append(where, "closed_at IS NOT NULL")
	}
	if f.Since != "" {
		where = append(where, "closed_at >= ?")
		args = append(args, f.Since)
	}
	where, args = f.clause(where, args)
	return where, args
}

// ListTrackerIssues returns the issues matching the filter, in id order —
// which groups them by tracker, since the tracker is the id's prefix — or
// oldest closure first when the filter is about closures.
func (s *Store) ListTrackerIssues(ctx context.Context, filter IssueFilter) ([]*TrackerIssue, error) {
	fields, err := fieldsOf(&TrackerIssue{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	where, args := filter.clauses()
	query := fmt.Sprintf("SELECT %s FROM tracker_issue", strings.Join(columns, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	switch {
	case !filter.Sort.Empty():
		query += " ORDER BY " + filter.Sort.SQL("")
	case filter.aboutClosures():
		query += " ORDER BY closed_at, id"
	default:
		query += " ORDER BY id"
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing tracker issues: %w", err)
	}
	defer rows.Close()

	var issues []*TrackerIssue
	for rows.Next() {
		var issue TrackerIssue
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&issue)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing tracker issues: %w", err)
		}
		issues = append(issues, &issue)
	}
	return issues, rows.Err()
}

// IssuesByProject returns each project's issues, keyed by project id.
//
// One query rather than one per project: the status page reads this for every
// row it draws, and a per-row lookup is the shape that turns a page render
// into N round trips.
func (s *Store) IssuesByProject(ctx context.Context) (map[string][]*TrackerIssue, error) {
	fields, err := fieldsOf(&TrackerIssue{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = "i." + f.column
	}

	query := fmt.Sprintf(
		"SELECT j.project_id, %s FROM project_tracker_issue j "+
			"JOIN tracker_issue i ON i.id = j.issue_id ORDER BY j.project_id, i.id",
		strings.Join(columns, ", "))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading issue links: %w", err)
	}
	defer rows.Close()

	byProject := map[string][]*TrackerIssue{}
	for rows.Next() {
		var projectID string
		var issue TrackerIssue
		dest := make([]any, 0, len(fields)+1)
		dest = append(dest, &projectID)
		for _, f := range fields {
			dest = append(dest, f.pointerOf(&issue))
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading issue links: %w", err)
		}
		copied := issue
		byProject[projectID] = append(byProject[projectID], &copied)
	}
	return byProject, rows.Err()
}

// IssueKeys returns the keys of every issue held for one tracker.
//
// Every issue, which is no longer what a sync asks for: IssuesToPoll is the
// scheduled subset, and this is the whole set behind it. Kept apart because
// they answer different questions — "what is recorded" does not change with
// the clock, and a caller wanting one and getting the other would be wrong in
// a way nothing would report.
func (s *Store) IssueKeys(ctx context.Context, tracker string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT key FROM tracker_issue WHERE tracker = ? ORDER BY key", tracker)
	if err != nil {
		return nil, fmt.Errorf("reading %s issues: %w", tracker, err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("reading %s issues: %w", tracker, err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// OpenActionsForIssue returns the open actions on every project that tracks an
// issue.
//
// The question behind "this closed, and there is still work against it". A
// project may track several issues and an issue may be tracked by several
// projects, so this is deliberately the union rather than an attempt to say
// which action belongs to which issue — nothing records that, and guessing
// would put the wrong work in the message.
func (s *Store) OpenActionsForIssue(ctx context.Context, issueID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT a.id
		FROM project_tracker_issue j
		JOIN action a ON a.project_id = j.project_id
		WHERE j.issue_id = ? AND a.closed_at IS NULL
		ORDER BY a.n`, issueID)
	if err != nil {
		return nil, fmt.Errorf("reading the open actions against %s: %w", issueID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("reading the open actions against %s: %w", issueID, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
