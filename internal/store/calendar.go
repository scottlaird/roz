package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DateFormat is how a calendar window's dates are stored: a plain day, no
// time and no zone. They are compared as text, which works because the format
// sorts correctly.
const DateFormat = "2006-01-02"

// Window kinds.
const (
	WindowOncall  = "oncall"
	WindowPTO     = "pto"
	WindowHoliday = "holiday"
	WindowOther   = "other"
)

// Capacity is how much of someone is left during a window.
//
// It is an enum and not a boolean on purpose: oncall is not a block, PTO is,
// and a sort that cannot tell them apart reads an oncall week as unavailable.
const (
	CapacityNone    = "none"
	CapacityReduced = "reduced"
	CapacityFull    = "full"
)

// WindowKinds are the kinds the interface suggests. The column takes any
// text: nothing branches on kind, so a closed set would have meant rebuilding
// the table for each new one. The list is here so a typo is remarked on where
// it is made.
var WindowKinds = []string{WindowOncall, WindowPTO, WindowHoliday, WindowOther}

// Capacities are the accepted values, matching the CHECK that is still on
// that column. That set stays closed because the sort reads it.
var Capacities = []string{CapacityNone, CapacityReduced, CapacityFull}

// CalendarWindow is a block of time that changes what can be taken on:
// oncall, PTO, a holiday.
//
// It is hand-entered at the weekly review rather than synced — calendar
// authentication is not worth the code for a handful of multi-day blocks — so
// every column is authored and there is nothing here for sync to write.
//
// EndsOn is INCLUSIVE. Google's all-day events carry an exclusive end date,
// so a window copied from one by hand is a day long unless the difference is
// noticed. The column says so, and so does the command that sets it.
//
// The table is calendar_window rather than window because window is reserved
// in SQLite.
type CalendarWindow struct {
	ID string `db:"id" kind:"identity"`

	Kind     string `db:"kind"`
	Label    string `db:"label"`
	StartsOn string `db:"starts_on"`
	EndsOn   string `db:"ends_on"`
	Capacity string `db:"capacity"`
	Note     string `db:"note"`

	CreatedAt string `db:"created_at" kind:"created"`
}

func (w *CalendarWindow) table() string       { return "calendar_window" }
func (w *CalendarWindow) subjectType() string { return "calendar_window" }
func (w *CalendarWindow) subjectID() string   { return w.ID }

// NewCalendarWindow returns an unsaved window.
//
// The identifier is derived from the kind and start date — oncall-2026-08-10
// — rather than allocated from a sequence. These are few, hand-entered and
// short-lived, so a readable name someone can type beats a number that has to
// be looked up. A caller wanting something else sets ID afterwards.
func NewCalendarWindow(kind, startsOn string) *CalendarWindow {
	return &CalendarWindow{
		ID:       WindowID(kind, startsOn),
		Kind:     kind,
		StartsOn: startsOn,
		Capacity: CapacityFull,
	}
}

// WindowID builds the default identifier for a window.
func WindowID(kind, startsOn string) string {
	return kind + "-" + startsOn
}

// ValidateDate checks a plain YYYY-MM-DD day.
//
// A timestamp is refused rather than truncated: these are whole days, and
// accepting a time would put a value in the column that the comparisons
// elsewhere do not expect.
func ValidateDate(field, value string) (string, error) {
	if _, err := time.Parse(DateFormat, value); err != nil {
		return "", fmt.Errorf("%s %q is not a date: use YYYY-MM-DD", field, value)
	}
	return value, nil
}

// IsSuggestedKind reports whether a kind is one of the familiar four.
//
// It is deliberately not a Validate: the column takes any text, so an
// unfamiliar kind is worth remarking on but not refusing. A caller that
// refuses one has decided a typo is likelier than a new kind, which is a
// judgement about its own users rather than a rule about the data.
func IsSuggestedKind(kind string) bool {
	for _, k := range WindowKinds {
		if kind == k {
			return true
		}
	}
	return false
}

// ValidateCapacity checks a capacity. This one the database enforces too,
// reporting what is accepted rather than only that the value was not.
func ValidateCapacity(capacity string) error {
	return validateOneOf("capacity", capacity, Capacities)
}

func validateOneOf(what, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("%s %q is not recognised: use %s", what, value, strings.Join(allowed, ", "))
}

// LoadCalendarWindow reads a window by id, returning sql.ErrNoRows if there
// is none.
func (t *Tx) LoadCalendarWindow(ctx context.Context, id string) (*CalendarWindow, error) {
	var w CalendarWindow
	if err := t.Load(ctx, &w, id); err != nil {
		return nil, err
	}
	return &w, nil
}

// Clone returns a copy to mutate, leaving the original as the before image
// for Tx.Update.
func (w *CalendarWindow) Clone() *CalendarWindow {
	clone := *w
	return &clone
}

// Covers reports whether a day falls inside the window, inclusive at both
// ends.
func (w *CalendarWindow) Covers(day string) bool {
	return w.StartsOn <= day && day <= w.EndsOn
}

// WindowFilter narrows ListCalendarWindows. The zero value selects
// everything.
type WindowFilter struct {
	// On keeps windows covering one day, inclusive.
	On string
	// Current keeps windows covering today.
	Current bool
	// Upcoming keeps windows that have not ended yet, which is what the
	// weekly review wants to look at.
	Upcoming bool
	// Kind keeps one kind.
	Kind string
	// Through keeps windows that begin on or before a date, which with
	// Upcoming gives "what is coming in the next fortnight" — the horizon a
	// status page cares about.
	Through string
}

// ListCalendarWindows returns windows matching the filter, earliest first.
func (s *Store) ListCalendarWindows(ctx context.Context, filter WindowFilter) ([]*CalendarWindow, error) {
	fields, err := fieldsOfStruct(&CalendarWindow{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	where, args := filter.clauses(s.now().UTC().Format(DateFormat))
	query := fmt.Sprintf("SELECT %s FROM calendar_window", strings.Join(columns, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY starts_on, ends_on, id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing calendar windows: %w", err)
	}
	defer rows.Close()

	var windows []*CalendarWindow
	for rows.Next() {
		var w CalendarWindow
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&w)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing calendar windows: %w", err)
		}
		windows = append(windows, &w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing calendar windows: %w", err)
	}
	return windows, nil
}

// clauses renders the filter as SQL. Dates compare as text, which is correct
// for YYYY-MM-DD, and the comparisons are inclusive at both ends because
// ends_on is.
func (f WindowFilter) clauses(today string) ([]string, []any) {
	var where []string
	var args []any

	if day := f.On; day != "" {
		where = append(where, "starts_on <= ? AND ends_on >= ?")
		args = append(args, day, day)
	}
	if f.Current {
		where = append(where, "starts_on <= ? AND ends_on >= ?")
		args = append(args, today, today)
	}
	if f.Upcoming {
		where = append(where, "ends_on >= ?")
		args = append(args, today)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Through != "" {
		where = append(where, "starts_on <= ?")
		args = append(args, f.Through)
	}
	return where, args
}
