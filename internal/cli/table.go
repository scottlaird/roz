package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

// Listings used to be a Printf per table: the header in one string, the row
// in another, and the two kept in step by hand. That is fine for one table and
// stops being fine at fifteen, which is what scottlaird/roz#169 is about —
// getting the right information out of a listing needs the columns to be
// nameable, and nothing could name them.
//
// So a listing declares its columns instead. What that buys immediately is
// --fields and a second output format; what it buys next is --sort over
// columns and, after that, --filter, both of which need the same thing this
// does: a place where the columns of a listing are written down.

// column is one thing a listing can show.
//
// A column is either a column of the record — named by field, rendered from
// the record's JSON like every other output — or derived, with a render
// function. The derived ones are what reflection cannot reach: how late an
// action is lives in a map beside the record, and a commit is shown at eight
// characters rather than forty.
type column[T any] struct {
	// name is what --fields calls it, and matches the db column where there
	// is one.
	name string
	// header is the table's heading. Empty takes the name, upper-cased with
	// its underscores opened out, which is what the hand-written tables
	// already spelled: first_seen became FIRST SEEN.
	header string
	// field is the record column this column shows. It is filled in from the
	// name where the name is one, so a declaration only has to say what it is
	// changing. Empty after that means genuinely derived: there is no column
	// of the record behind it.
	field string
	// render is how the cell reads in a table, and is presentation only.
	//
	// The rule it follows: a column that has a field carries that field's
	// value everywhere except the table. A commit shown at eight characters
	// is a commit shown at eight characters, not a different commit, so CSV
	// and JSON give the commit and choosing fewer columns can never alter
	// one of them.
	//
	// For a derived column, with no field behind it, this is the value.
	render func(rec T, ctx renderContext) string
	// showIf keeps a column out of the default view when nothing in the
	// result is using it — the rule BECAUSE and PIPELINE already follow, and
	// the reason a listing of open pull requests has no MERGED column. It
	// applies to the default view only: a column asked for by name is shown
	// whatever is in it, because asking is the answer to "is this relevant".
	showIf func(rows []T) bool
}

// renderContext is what a cell needs and the record does not carry.
//
// now is stamped once for the whole table rather than read per cell, which is
// what stops one row from calling a snooze due and the next from not, and is
// what writeActionTable already did by hand.
type renderContext struct {
	now string
}

// columnSet is everything one listing can show.
//
// Only the derived and the overridden columns are declared. The rest are the
// record's own, read off the same struct tags the SELECT and the JSON encoder
// read, so adding a column to an entity makes it available to --fields without
// anything here being touched.
type columnSet[T any] struct {
	// blank is an empty record, which is how the columns are named when
	// there are no rows to read them off.
	blank T
	// declared holds the derived columns and the overrides.
	declared []column[T]
	// defaults names the default view, in order. Everything else is
	// reachable only by asking for it.
	defaults []string
	// empty is what a table says when there is nothing, e.g. "no refs".
	empty string
}

// all resolves the full vocabulary: every column of the record in struct
// order, with any declaration overriding how one is shown, then the derived
// columns that are not record columns at all.
func (s columnSet[T]) all() ([]column[T], error) {
	names, err := store.Columns(s.blank)
	if err != nil {
		return nil, err
	}
	declared := make(map[string]column[T], len(s.declared))
	for _, c := range s.declared {
		declared[c.name] = c
	}

	resolved := make([]column[T], 0, len(names)+len(s.declared))
	for _, name := range names {
		c, ok := declared[name]
		if !ok {
			resolved = append(resolved, column[T]{name: name, field: name})
			continue
		}
		// Naming a record column is what says this one has a value behind it,
		// so the declaration only has to describe what it changes about how
		// that value is shown.
		if c.field == "" {
			c.field = name
		}
		resolved = append(resolved, c)
		delete(declared, name)
	}
	// Whatever is left is derived, in the order it was declared — an info
	// dump has to put them somewhere, and after the real columns is the
	// least surprising place.
	for _, c := range s.declared {
		if _, ok := declared[c.name]; ok {
			resolved = append(resolved, c)
		}
	}
	return resolved, nil
}

// names is the vocabulary, for an error that says what was available.
func (s columnSet[T]) names() []string {
	all, err := s.all()
	if err != nil {
		return nil
	}
	names := make([]string, len(all))
	for i, c := range all {
		names[i] = c.name
	}
	return names
}

// selected resolves what to show: the named fields, or the default view.
//
// fieldsAll is the info dump the issue asked for — every column the record
// defines, which is otherwise unreachable because the default view is a
// deliberate subset.
func (s columnSet[T]) selected(fields []string, rows []T) ([]column[T], error) {
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]column[T], len(all))
	for _, c := range all {
		byName[c.name] = c
	}

	if len(fields) == 1 && fields[0] == fieldsAll {
		return all, nil
	}
	if len(fields) == 0 {
		chosen := make([]column[T], 0, len(s.defaults))
		for _, name := range s.defaults {
			c, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("the default view names %q, which is not a column", name)
			}
			if c.showIf != nil && !c.showIf(rows) {
				continue
			}
			chosen = append(chosen, c)
		}
		return chosen, nil
	}

	chosen := make([]column[T], 0, len(fields))
	for _, name := range fields {
		c, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("--%s %q is not a column: use %s, or %s for all of them",
				flagFields, name, strings.Join(s.names(), ", "), fieldsAll)
		}
		chosen = append(chosen, c)
	}
	return chosen, nil
}

// heading is the column's header, defaulting to its name.
func (c column[T]) heading() string {
	if c.header != "" {
		return c.header
	}
	return strings.ToUpper(strings.ReplaceAll(c.name, "_", " "))
}

// writeRecords renders a listing in whichever format was asked for.
//
// The rows are rendered from the marshalled JSON rather than from a second
// pass over the struct, which is what writeDetail does for a single record and
// for the same reason: whatever the encoder put in, this lays out. A
// format:"json" column arrives as the structure it holds, a NULL as null, and
// nothing here has to know which columns those are.
func writeRecords[T any](out io.Writer, format string, set columnSet[T], fields []string, rows []T) error {
	columns, err := set.selected(fields, rows)
	if err != nil {
		return err
	}

	encoded, err := store.MarshalRecords(rows)
	if err != nil {
		return err
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &objects); err != nil {
		return err
	}
	if len(objects) != len(rows) {
		return fmt.Errorf("marshalled %d records into %d objects", len(rows), len(objects))
	}

	ctx := renderContext{now: time.Now().UTC().Format(store.TimeFormat)}

	switch format {
	case outputJSON:
		// Untouched when nothing was asked for, so `-o json` keeps carrying
		// everything: something parsing it should not lose a column because
		// the table's default view does not show one.
		if len(fields) == 0 {
			_, err := fmt.Fprintln(out, string(encoded))
			return err
		}
		return writeJSONRecords(out, columns, objects, rows, ctx)
	case outputCSV:
		return writeCSVRecords(out, columns, objects, rows, ctx)
	default:
		return writeTableRecords(out, set.empty, columns, objects, rows, ctx)
	}
}

// tableCell renders one column of one row for a reader: presentation, so a
// declared render wins.
func tableCell[T any](c column[T], object map[string]json.RawMessage, rec T, ctx renderContext) string {
	if c.render != nil {
		return c.render(rec, ctx)
	}
	return renderCell(object[c.field], "-")
}

// dataCell renders one column of one row for something else to read, where
// the record's own value is what is wanted and the table's presentation of it
// is not.
//
// An absent value is empty rather than the table's dash: a dash is a mark for
// a reader, and a spreadsheet would take it for data.
func dataCell[T any](c column[T], object map[string]json.RawMessage, rec T, ctx renderContext) string {
	if c.field != "" {
		return renderCell(object[c.field], "")
	}
	return c.render(rec, ctx)
}

func writeTableRecords[T any](out io.Writer, empty string, columns []column[T],
	objects []map[string]json.RawMessage, rows []T, ctx renderContext) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, empty)
		return err
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	headers := make([]string, len(columns))
	for i, c := range columns {
		headers[i] = c.heading()
	}
	fmt.Fprintln(w, strings.Join(headers, "\t"))

	for i, rec := range rows {
		cells := make([]string, len(columns))
		for j, c := range columns {
			cells[j] = tableCell(c, objects[i], rec, ctx)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	return w.Flush()
}

// writeCSVRecords writes the header even when there are no rows, so something
// reading the output finds the shape it expected rather than nothing at all.
//
// Cells carry the record's values, not the table's rendering of them, for the
// reason the JSON does: this output is for something else to read, and a
// commit abbreviated for a human to skim is not what a spreadsheet wants. The
// header is the column names for the same reason.
func writeCSVRecords[T any](out io.Writer, columns []column[T],
	objects []map[string]json.RawMessage, rows []T, ctx renderContext) error {
	w := csv.NewWriter(out)

	headers := make([]string, len(columns))
	for i, c := range columns {
		headers[i] = c.name
	}
	if err := w.Write(headers); err != nil {
		return err
	}
	for i, rec := range rows {
		cells := make([]string, len(columns))
		for j, c := range columns {
			cells[j] = dataCell(c, objects[i], rec, ctx)
		}
		if err := w.Write(cells); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// writeJSONRecords narrows the objects to the columns asked for.
//
// A column with a field keeps that field's own JSON — its type, and null for
// absent — whatever the table does with it. So narrowing loses columns and
// never alters one, and `--fields commit_sha -o json` is the commit rather
// than the eight characters the table shows.
//
// A derived column has no field behind it, so the string it renders to is the
// only value there is, and that is what goes in.
func writeJSONRecords[T any](out io.Writer, columns []column[T],
	objects []map[string]json.RawMessage, rows []T, ctx renderContext) error {
	narrowed := make([]map[string]json.RawMessage, 0, len(rows))
	for i, rec := range rows {
		object := make(map[string]json.RawMessage, len(columns))
		for _, c := range columns {
			if c.field != "" {
				object[c.name] = objects[i][c.field]
				continue
			}
			rendered, err := json.Marshal(c.render(rec, ctx))
			if err != nil {
				return err
			}
			object[c.name] = rendered
		}
		narrowed = append(narrowed, object)
	}

	encoded, err := json.Marshal(narrowed)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(encoded))
	return err
}

// renderCell renders one column's JSON for a human: absent as absent, strings
// unquoted, and arrays or objects left in their JSON form.
//
// absent is what stands in for a null or an empty value, which differs by
// format: a table writes a dash so a reader can see there is nothing there,
// and a CSV writes nothing so a reader does not take the dash for data.
func renderCell(raw json.RawMessage, absent string) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	switch x := value.(type) {
	case nil:
		return absent
	case string:
		if x == "" {
			return absent
		}
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		if len(x) == 0 {
			return absent
		}
		// A list of names reads as a list of names. This is what an action's
		// blockers looked like when `show` built that row by hand, and
		// printing ["NA1","NA2"] instead would have been the relation
		// declarations costing legibility to buy consistency.
		if joined, ok := joinStrings(x); ok {
			return joined
		}
		return string(raw)
	default:
		return string(raw)
	}
}

// Field selection.

const (
	flagFields = "fields"
	// fieldsAll is the info dump: every column the record defines, which the
	// default view deliberately does not show.
	fieldsAll = "all"
)

// addFieldsFlag registers --fields on a listing that declares its columns.
func addFieldsFlag(cmd *cobra.Command, set interface{ names() []string }) {
	cmd.Flags().String(flagFields, "",
		"columns to show, comma-separated: "+strings.Join(set.names(), ", ")+
			", or "+fieldsAll+"; unset shows the usual ones")
}

// fieldsFrom reads --fields, returning nil when it was not given.
func fieldsFrom(cmd *cobra.Command) ([]string, error) {
	value, err := cmd.Flags().GetString(flagFields)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	fields := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			// A trailing comma is a typo rather than a request for a blank
			// column, and silently dropping it would hide the typo.
			return nil, fmt.Errorf("--%s %q has an empty name in it", flagFields, value)
		}
		fields = append(fields, name)
	}
	return fields, nil
}
