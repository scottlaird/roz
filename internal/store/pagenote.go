package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Slots are the places on the page a note can go, in the order they appear.
//
// A closed set, and the reason is the last caveat on the feature: a note under
// a key nothing renders is invisible rather than wrong, and there would be no
// error to say so. Refusing an unknown key turns that silence into a message.
//
// It is also what keeps this from becoming a document store. The page is worth
// reading because almost all of it is derived; four places to put prose is a
// page with some prose on it, and arbitrary keys would be somewhere to put
// anything.
const (
	SlotIntro          = "intro"
	SlotBeforeQueue    = "before-queue"
	SlotBeforeProjects = "before-projects"
	SlotFooter         = "footer"
)

// Slots is the vocabulary, in page order.
var Slots = []string{SlotIntro, SlotBeforeQueue, SlotBeforeProjects, SlotFooter}

// ValidateSlot refuses a key the page would never draw.
func ValidateSlot(key string) error {
	for _, slot := range Slots {
		if key == slot {
			return nil
		}
	}
	return fmt.Errorf("%q is not a slot on the page: use %s",
		key, strings.Join(Slots, ", "))
}

// PageNote is one keyed prose block.
//
// Markdown, like every other prose field, and linked the same way — an
// introduction that mentions SL7 should link it for the same reason a
// project summary does.
type PageNote struct {
	Key  string `db:"key" kind:"identity"`
	Body string `db:"body" format:"markdown"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`
}

func (n *PageNote) table() string       { return "page_note" }
func (n *PageNote) subjectType() string { return "page_note" }
func (n *PageNote) subjectID() string   { return n.Key }

// keyColumn: keyed on the slot, not on a surrogate id.
func (n *PageNote) keyColumn() string { return "key" }

func (n *PageNote) Clone() *PageNote {
	clone := *n
	return &clone
}

// LoadPageNote reads one note. A missing key is sql.ErrNoRows; a slot nobody
// has written to is the ordinary state, so callers translate rather than fail.
func (t *Tx) LoadPageNote(ctx context.Context, key string) (*PageNote, error) {
	note := &PageNote{Key: key}
	if err := t.Load(ctx, note, key); err != nil {
		return nil, err
	}
	return note, nil
}

// SetPageNote writes a slot's prose, creating it the first time.
//
// Replacing rather than appending: a note is the current text for a slot, and
// the log already keeps what it said before.
func (s *Store) SetPageNote(ctx context.Context, actor Actor, key, body string) ([]Change, error) {
	if err := ValidateSlot(key); err != nil {
		return nil, err
	}

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var changes []Change
	before, err := tx.LoadPageNote(ctx, key)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := tx.Insert(ctx, &PageNote{Key: key, Body: body}); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		after := before.Clone()
		after.Body = body
		if changes, err = tx.Update(ctx, before, after); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changes, nil
}

// PageNotes returns every note written, keyed by slot.
//
// One query: the page asks for all of them and renders whichever slots have
// something, so a lookup per slot would be four round trips to draw nothing
// three times.
func (s *Store) PageNotes(ctx context.Context) (map[string]*PageNote, error) {
	fields, err := fieldsOf(&PageNote{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	query := fmt.Sprintf("SELECT %s FROM page_note", strings.Join(columns, ", "))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading page notes: %w", err)
	}
	defer rows.Close()

	notes := map[string]*PageNote{}
	for rows.Next() {
		var note PageNote
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&note)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading page notes: %w", err)
		}
		copied := note
		notes[note.Key] = &copied
	}
	return notes, rows.Err()
}
