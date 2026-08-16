package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// View is a listing somebody named.
//
// Which listing, which columns, which order, which filter — every part of it
// already existed as a flag. What it adds is the name, so a question worth
// asking twice is not retyped, and so the interesting ones can be long without
// being unusable.
//
// Adding one needs no code. A filter is an expression over the columns a
// listing already declares, so a new question is a row here rather than a
// flag, a query and a release. That is the whole argument for the table.
type View struct {
	Name   string `db:"name" kind:"identity"`
	Entity string `db:"entity"`

	// Filter, Sort and Fields are the three flags, stored as typed. Empty
	// means the view says nothing about that, which is not the same as saying
	// something empty: a view with no Sort leaves the listing's own order
	// alone rather than clearing it.
	Filter string `db:"filter"`
	Sort   string `db:"sort"`
	Fields string `db:"fields"`

	// Description is what it is for, in a sentence.
	//
	// Worth a column because the name is short by design and a filter is not
	// always legible: priority 1 is the highest priority, so "urgent" reads as
	// `priority <= 2`, which is backwards to anybody who has not been told.
	Description string `db:"description" format:"markdown"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`
}

func (v *View) table() string       { return "view" }
func (v *View) subjectType() string { return "view" }
func (v *View) subjectID() string   { return v.Name }

// keyColumn: a view is referred to by what it is for, not as one of many
// similar things, so the name is the key.
func (v *View) keyColumn() string { return "name" }

func (v *View) Clone() *View {
	clone := *v
	return &clone
}

// ValidateViewName refuses what the table cannot hold, before a write reaches
// a CHECK that can only say the row is bad.
//
// A lower-case identifier: `^[a-z][a-z0-9_]*$`. The rule is not "no spaces" but
// "nothing that has to be escaped" — a name is an argument, and a quote, a
// glob character or an emoji all make one that cannot be typed without
// wrapping it in something. Starting with a letter keeps it from reading as a
// number or a flag.
//
// Scanned rather than matched against a pattern, so the error can name the
// character that stopped it. "q4 is fine and q-4 is not" is a more useful
// thing to be told than a regex.
func ValidateViewName(name string) error {
	if name == "" {
		return fmt.Errorf("a view needs a name: it is what you type to use it")
	}
	if first := name[0]; first < 'a' || first > 'z' {
		return fmt.Errorf(
			"view name %q starts with %q; a name starts with a lower-case letter, "+
				"so it does not read as a number or a flag", name, string(name[0]))
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return fmt.Errorf(
				"view name %q holds %q; a name is an argument, so it holds "+
					"lower-case letters, digits and underscores — anything else "+
					"has to be quoted to be typed", name, string(c))
		}
	}
	return nil
}

// LoadView reads one view, sql.ErrNoRows when there is none.
func (t *Tx) LoadView(ctx context.Context, name string) (*View, error) {
	v := &View{Name: name}
	if err := t.Load(ctx, v, name); err != nil {
		return nil, err
	}
	return v, nil
}

// SaveView writes a view, creating it the first time.
//
// Whole rather than field by field: a view is short, and "the view is now
// this" is the only edit anybody makes to one. What it was is in the log.
func (s *Store) SaveView(ctx context.Context, actor Actor, v *View) ([]Change, error) {
	if err := ValidateViewName(v.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(v.Entity) == "" {
		return nil, fmt.Errorf("view %q does not say which listing it is of", v.Name)
	}

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var changes []Change
	before, err := tx.LoadView(ctx, v.Name)
	switch {
	case err == sql.ErrNoRows:
		if err := tx.Insert(ctx, v); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		after := before.Clone()
		after.Entity, after.Filter = v.Entity, v.Filter
		after.Sort, after.Fields = v.Sort, v.Fields
		after.Description = v.Description
		if changes, err = tx.Update(ctx, before, after); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changes, nil
}

// DropView forgets a view. The listing it was of is untouched.
func (s *Store) DropView(ctx context.Context, actor Actor, name string) error {
	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadView(ctx, name)
	if err != nil {
		return err
	}
	if _, err := tx.tx.ExecContext(ctx, "DELETE FROM view WHERE name = ?", name); err != nil {
		return fmt.Errorf("dropping view %s: %w", name, err)
	}
	if err := tx.emit(ctx, before, event{
		kind: eventChanged, field: "view", oldValue: before.Entity, newValue: "",
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// Views returns every saved view, or those of one listing.
//
// Ordered by name because that is what somebody scans: a view is found by
// remembering roughly what it was called.
func (s *Store) Views(ctx context.Context, entity string) ([]*View, error) {
	fields, err := fieldsOf(&View{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	query := fmt.Sprintf("SELECT %s FROM view", strings.Join(columns, ", "))
	var args []any
	if entity != "" {
		query += " WHERE entity = ?"
		args = append(args, entity)
	}
	query += " ORDER BY name"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading views: %w", err)
	}
	defer rows.Close()

	var views []*View
	for rows.Next() {
		var v View
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&v)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading views: %w", err)
		}
		copied := v
		views = append(views, &copied)
	}
	return views, rows.Err()
}
