package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Severity is kept apart from an event's kind so a monitor can filter on
// severity alone, without knowing a kind vocabulary that will grow.
const (
	SeverityInfo      = "info"
	SeverityNotice    = "notice"
	SeverityException = "exception"
)

// Event kinds written by this package. The vocabulary is open — sync sources
// and monitors add their own — which is why severity is a separate column.
const (
	eventCreated = "created"
	eventChanged = "changed"
	eventNote    = "note"
)

// Event is one row of the log, as read back.
//
// Every column is tagged identity because the log is append-only: a row is
// written once and never updated or deleted. Event is not a Record — it has
// no id of its own and nothing loads one by identifier — but it carries the
// same db tags, so it marshals through the same path as an entity.
type Event struct {
	Seq         int64  `db:"seq" kind:"identity"`
	At          string `db:"at" kind:"identity"`
	Correlation string `db:"correlation" kind:"identity"`
	Actor       string `db:"actor" kind:"identity"`
	Kind        string `db:"kind" kind:"identity"`
	Severity    string `db:"severity" kind:"identity"`
	SubjectType string `db:"subject_type" kind:"identity"`
	SubjectID   string `db:"subject_id" kind:"identity"`
	Field       string `db:"field" kind:"identity"`
	OldValue    string `db:"old_value" kind:"identity"`
	NewValue    string `db:"new_value" kind:"identity"`
	Note        string `db:"note" kind:"identity"`
	Payload     string `db:"payload" kind:"identity" format:"json"`
}

// EventQuery selects a window of the log. The zero value selects everything,
// oldest first.
type EventQuery struct {
	// Severity keeps only events at one severity — the filter the schema
	// separated severity from kind to make possible, so a monitor can watch
	// for exceptions without knowing the kind vocabulary.
	Severity string
	// Kind keeps only one event kind. The vocabulary is open, so this is a
	// plain match rather than a checked enum.
	Kind string
	// ExcludeActors drops events written by any of these actors, which is how
	// a reader hides its own writes without having to name every other actor
	// in advance. Exclusion rather than selection because the question is
	// "everything but mine", and an allow-list cannot express that without
	// knowing the whole vocabulary — which is open.
	ExcludeActors []string
	// AfterSeq excludes everything at or below a sequence number. This is the
	// cursor a tail advances.
	AfterSeq int64
	// SinceAt excludes everything before a timestamp, inclusive.
	SinceAt string
	// Newest takes the last n matching rows instead of the first. The result
	// is still returned oldest first.
	Newest int
	// Where is a WHERE fragment somebody else compiled — the SQL half of a
	// CEL filter. Opaque here: what it means is the filter package's
	// business.
	//
	// It matters more on the log than on a listing. A tail re-runs its query
	// every interval, so a predicate that ran in Go would read every new row
	// and discard most of them, for ever, rather than once.
	Where string
	// WhereArgs are its parameters, in the order the fragment names them.
	WhereArgs []any
}

// Events reads the log, always returning rows oldest first so a caller can
// print them in order and take the last Seq as its cursor.
func (s *Store) Events(ctx context.Context, q EventQuery) ([]*Event, error) {
	fields, err := fieldsOfStruct(&Event{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	where, args := q.clauses()
	query := fmt.Sprintf("SELECT %s FROM event", strings.Join(columns, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if q.Newest > 0 {
		query += " ORDER BY seq DESC LIMIT ?"
		args = append(args, q.Newest)
	} else {
		query += " ORDER BY seq"
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the log: %w", err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		var e Event
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&e)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading the log: %w", err)
		}
		events = append(events, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the log: %w", err)
	}

	if q.Newest > 0 {
		reverse(events)
	}
	return events, nil
}

// LatestEventSeq returns the highest sequence number in the log, or 0 when it
// is empty.
//
// A tail takes this as its starting cursor rather than deriving one from the
// backlog it printed: the backlog is filtered and may be empty, and resuming
// from 0 would replay the whole log on the first poll.
func (s *Store) LatestEventSeq(ctx context.Context) (int64, error) {
	var seq int64
	// coalesce, because max() over no rows is NULL rather than 0.
	err := s.db.QueryRowContext(ctx, "SELECT coalesce(max(seq), 0) FROM event").Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("reading the log position: %w", err)
	}
	return seq, nil
}

func (q EventQuery) clauses() ([]string, []any) {
	var where []string
	var args []any

	if q.Severity != "" {
		where = append(where, "severity = ?")
		args = append(args, q.Severity)
	}
	if q.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, q.Kind)
	}
	if q.AfterSeq > 0 {
		where = append(where, "seq > ?")
		args = append(args, q.AfterSeq)
	}
	if q.SinceAt != "" {
		where = append(where, "at >= ?")
		args = append(args, q.SinceAt)
	}
	if q.Where != "" {
		where = append(where, "("+q.Where+")")
		args = append(args, q.WhereArgs...)
	}
	if len(q.ExcludeActors) > 0 {
		placeholders := make([]string, len(q.ExcludeActors))
		for i, actor := range q.ExcludeActors {
			placeholders[i] = "?"
			args = append(args, actor)
		}
		where = append(where, "actor NOT IN ("+strings.Join(placeholders, ", ")+")")
	}
	return where, args
}

func reverse(events []*Event) {
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
}

// event is one row of the log: a single field change, or a lifecycle event
// with no field.
type event struct {
	kind     string
	severity string
	field    string
	oldValue string
	newValue string
	note     string
	payload  string
}

const insertEvent = `
INSERT INTO event (at, correlation, actor, kind, severity,
                   subject_type, subject_id, field, old_value, new_value, note, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// emit appends one row to the log. The log is append-only: nothing here ever
// updates or deletes, and there are no foreign keys, so a subject can be
// deleted without disturbing its history.
func (t *Tx) emit(ctx context.Context, r Record, e event) error {
	if e.severity == "" {
		e.severity = SeverityInfo
	}
	if e.payload == "" {
		e.payload = "{}"
	}

	_, err := t.tx.ExecContext(ctx, insertEvent,
		t.at, t.correlation, string(t.actor), e.kind, e.severity,
		r.subjectType(), r.subjectID(), e.field, e.oldValue, e.newValue, e.note, e.payload)
	if err != nil {
		return fmt.Errorf("logging %s event for %s: %w", e.kind, r.subjectID(), err)
	}
	return nil
}

// Note appends a note to a subject's history without changing it.
func (t *Tx) Note(ctx context.Context, r Record, text string) error {
	return t.emit(ctx, r, event{kind: eventNote, note: text})
}

// Exception records that something needs a human's attention — an action in
// wait_review that just acquired unresolved comments, say. It changes
// nothing; a monitor tailing the log is what acts on it.
func (t *Tx) Exception(ctx context.Context, r Record, kind, note string) error {
	return t.emit(ctx, r, event{
		kind:     kind,
		severity: SeverityException,
		note:     note,
	})
}

// ExceptionInterval is how long a standing condition stays quiet after it has
// been reported.
//
// A day, because these describe situations rather than events: a repository
// too large for the ref feed is the same fact tomorrow as it is today, and one
// notice a day is the most that can be acted on. Sync polls every few seconds,
// so without this a single standing condition writes thousands of identical
// lines a day into the log a monitor watches.
const ExceptionInterval = 24 * time.Hour

// ExceptionOnce records an exception unless the same condition has already
// been reported recently, and says whether it wrote one.
//
// For conditions observed by polling rather than at a transition. Sync cannot
// tell "this just became true" from "this is still true": it re-derives the
// world every few seconds, and every derivation finds the same unreadable
// repository. Reporting each one buries the log in restatements and teaches
// whoever reads it to skip that source — which costs more than the repeats,
// because the first notice was worth having.
//
// The key is the condition — kind, subject type, subject id — and never the
// message. Two problems on one repository are two kinds and stay separate,
// while rewording a message does not defeat the suppression.
//
// The log is the record, so nothing else has to remember. Same reasoning as
// the overdue query, which asks the same question about its own deadline.
func (t *Tx) ExceptionOnce(ctx context.Context, r Record, kind, note string) (bool, error) {
	since, err := time.Parse(timeFormat, t.at)
	if err != nil {
		return false, fmt.Errorf("reading the transaction clock: %w", err)
	}
	cutoff := since.Add(-ExceptionInterval).UTC().Format(timeFormat)

	var reported int
	err = t.tx.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM event
		  WHERE severity = ? AND kind = ? AND subject_type = ? AND subject_id = ?
		    AND at >= ?)`,
		SeverityException, kind, r.subjectType(), r.subjectID(), cutoff).Scan(&reported)
	if err != nil {
		return false, fmt.Errorf("checking whether %s was already reported for %s: %w",
			kind, r.subjectID(), err)
	}
	if reported == 1 {
		return false, nil
	}
	return true, t.Exception(ctx, r, kind, note)
}
