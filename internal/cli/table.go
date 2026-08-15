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
	//
	// It takes the context for the same reason render does: whether a column
	// has anything to say is not always a question the rows can answer on
	// their own. Whether an action is held depends on its project's status,
	// which is beside the rows rather than in them.
	showIf func(rows []T, ctx renderContext) bool
}

// renderContext is what a cell needs and the record does not carry.
//
// now is stamped once for the whole table rather than read per cell, which is
// what stops one row from calling a snooze due and the next from not, and is
// what writeActionTable already did by hand. It is filled in by the renderer,
// so a caller only supplies the rest.
type renderContext struct {
	now string
	// late is how many days past its allowance each action is, keyed by
	// identifier. Absence is what says not late, so the map is read for
	// presence rather than for its number — see lateCell.
	late map[string]int
	// blocked is the set of projects that are, keyed by identifier. A blocked
	// project's actions are out of the queue, and a listing that showed them
	// unmarked would be the disagreement scottlaird/roz#181 was about, moved
	// one place along.
	blocked map[string]bool
}

// columnSet is everything one listing can show.
//
// Only the derived and the overridden columns are declared. The rest are the
// record's own, read off the same struct tags the SELECT and the JSON encoder
// read, so adding a column to an entity makes it available to --fields without
// anything here being touched.
type columnSet[T any] struct {
	// blank is an empty row, which is how the columns are named when there
	// are no rows to read them off.
	blank T
	// record reaches the db-tagged record inside a row, for a listing whose
	// rows carry something besides it — `project list --tree` pairs a project
	// with its depth. Nil means the row is the record.
	record func(T) any
	// declared holds the derived columns and the overrides.
	declared []column[T]
	// defaults names the default view, in order. Everything else is
	// reachable only by asking for it.
	defaults []string
	// rankings are orderings this listing has that are not columns, and are
	// what --sort accepts besides a column list.
	//
	// The queue's are the case: priority is a CTE, two joins and an
	// expression over three tables, and staleness is a computed date. Neither
	// is a column, so neither can be a key in a list of them — and both are
	// what somebody actually types, so they keep their names.
	rankings []string
	// empty is what a table says when there is nothing, e.g. "no refs".
	empty string
}

// all resolves the full vocabulary: every column of the record in struct
// order, with any declaration overriding how one is shown, then the derived
// columns that are not record columns at all.
// recordOf reaches the db-tagged record inside a row.
func (s columnSet[T]) recordOf(row T) any {
	if s.record != nil {
		return s.record(row)
	}
	return row
}

func (s columnSet[T]) all() ([]column[T], error) {
	names, err := store.Columns(s.recordOf(s.blank))
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
func (s columnSet[T]) selected(fields []string, rows []T, ctx renderContext) ([]column[T], error) {
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
			if c.showIf != nil && !c.showIf(rows, ctx) {
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
func writeRecords[T any](out io.Writer, format string, set columnSet[T],
	fields []string, rows []T, ctx renderContext) error {
	// Stamped before the columns are chosen, since whether one is worth
	// showing can depend on what is in the context.
	ctx.now = time.Now().UTC().Format(store.TimeFormat)

	columns, err := set.selected(fields, rows, ctx)
	if err != nil {
		return err
	}

	records := make([]any, len(rows))
	for i, row := range rows {
		records[i] = set.recordOf(row)
	}
	encoded, err := store.MarshalRecords(records)
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

// cellStyle is how a value is spelled, which depends on who is reading it.
type cellStyle struct {
	// absent stands in for a null or an empty value. A table writes a dash so
	// a reader can see there is nothing there; CSV writes nothing, so a
	// reader does not take the dash for data.
	absent string
	// yesNo spells a boolean the way the hand-written tables did. It is a
	// table's spelling and not a value: something parsing the output wants
	// true and false.
	yesNo bool
}

var (
	// tableStyle is a listing, for a person to skim.
	tableStyle = cellStyle{absent: "-", yesNo: true}
	// dataStyle is CSV or JSON, for something else to read.
	dataStyle = cellStyle{absent: ""}
	// detailStyle is `show`, which is a person reading one record — and
	// which has always spelled a boolean true rather than yes. Kept that way
	// deliberately: converting the listings is not the moment to change what
	// a different command prints.
	detailStyle = cellStyle{absent: "-"}
)

// tableCell renders one column of one row for a reader: presentation, so a
// declared render wins.
func tableCell[T any](c column[T], object map[string]json.RawMessage, rec T, ctx renderContext) string {
	if c.render != nil {
		return orAbsent(c.render(rec, ctx), tableStyle)
	}
	return renderCell(object[c.field], tableStyle)
}

// orAbsent spells a rendered nothing the way the format spells one.
//
// A render function returns an empty string for "there is nothing here" and
// leaves how that reads to the format, so a not-late action is a dash in the
// table and an empty field in the CSV. Rendering the dash itself would put a
// mark meant for a reader into a column something else is going to parse.
func orAbsent(rendered string, style cellStyle) string {
	if rendered == "" {
		return style.absent
	}
	return rendered
}

// dataCell renders one column of one row for something else to read, where
// the record's own value is what is wanted and the table's presentation of it
// is not.
func dataCell[T any](c column[T], object map[string]json.RawMessage, rec T, ctx renderContext) string {
	if c.field != "" {
		return renderCell(object[c.field], dataStyle)
	}
	return orAbsent(c.render(rec, ctx), dataStyle)
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

// renderCell renders one column's JSON: absent as the style says, strings
// unquoted, and arrays or objects left in their JSON form.
func renderCell(raw json.RawMessage, style cellStyle) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	switch x := value.(type) {
	case nil:
		return style.absent
	case string:
		if x == "" {
			return style.absent
		}
		return x
	case bool:
		if !style.yesNo {
			return strconv.FormatBool(x)
		}
		return yesNo(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		if len(x) == 0 {
			return style.absent
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

// addListingFlags puts the output flags on a listing that declares its
// columns.
func addListingFlags[T any](cmd *cobra.Command, set columnSet[T]) {
	addListOutputFlag(cmd)
	addFieldsFlag(cmd, set)
	addFilterFlag(cmd, set.names())
}

// runListing is the tail every list command shares once its columns are
// declared: filter, choose the format, choose the fields, render.
//
// The filter runs here rather than in each command because most listings do
// not push it down: their store call takes no WHERE fragment, so the rows have
// already been read by the time this sees them. A listing that does push it
// down — `pr list` — compiles the same filter earlier and passes what is left,
// so this is where the residual lands either way.
func runListing[T any](cmd *cobra.Command, set columnSet[T], rows []T, ctx renderContext) error {
	f, err := filterFrom(cmd, set.recordOf(set.blank))
	if err != nil {
		return err
	}
	if err := explainFilter(cmd, f); err != nil {
		return err
	}
	if rows, err = keep(f, rows, set.recordOf); err != nil {
		return err
	}

	format, err := listOutputFrom(cmd)
	if err != nil {
		return err
	}
	fields, err := fieldsFrom(cmd)
	if err != nil {
		return err
	}
	return writeRecords(cmd.OutOrStdout(), format, set, fields, rows, ctx)
}

// sortKeys resolves --sort names into columns to order by.
//
// A name has to reach a column of the record. A derived column — how late an
// action is, the indentation of a title — has nothing to sort on, and a
// listing's own column names are what the error offers instead of leaving
// somebody to guess.
func (s columnSet[T]) sortKeys(names []string) ([]store.SortKey, error) {
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]column[T], len(all))
	for _, c := range all {
		byName[c.name] = c
	}
	sortable, err := store.Columns(s.recordOf(s.blank))
	if err != nil {
		return nil, err
	}
	isColumn := make(map[string]bool, len(sortable))
	for _, name := range sortable {
		isColumn[name] = true
	}

	keys := make([]store.SortKey, 0, len(names))
	for _, name := range names {
		key := store.SortKey{Column: strings.TrimPrefix(name, descendingPrefix)}
		key.Desc = key.Column != name

		c, ok := byName[key.Column]
		if ok && c.field != "" {
			// A column shown differently from what it holds still sorts on
			// what it holds: `pipeline list --sort steps` would otherwise ask
			// SQLite for a column that is not in the table.
			key.Column = c.field
		}
		if !isColumn[key.Column] {
			// The rankings belong in this error too. They are what --sort
			// accepts besides a column, so an error that names only the
			// columns would read as though `--sort priority` had stopped
			// working.
			offer := strings.Join(sortable, ", ")
			if len(s.rankings) > 0 {
				offer += "; or one of " + strings.Join(s.rankings, ", ")
			}
			return nil, fmt.Errorf("--%s %q is not a column to sort on: use %s",
				sortFlag, name, offer)
		}
		keys = append(keys, key)
	}
	return keys, nil
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
