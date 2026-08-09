package store

import (
	"context"
	"fmt"
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
