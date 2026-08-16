package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
)

// FieldKind says who may write a column and whether changing it is logged.
// It is the schema sketch's authored/observed split, made enforceable: it is
// declared once per column in a struct tag and checked in one place, rather
// than remembered at each call site.
//
// The values are the strings used in the kind struct tag.
type FieldKind string

const (
	// Authored is written by a human or an agent. Sync must never touch it.
	Authored FieldKind = "authored"
	// Observed is written only by sync, from an external system.
	Observed FieldKind = "observed"
	// Derived is computed by the database. Never written, never diffed.
	Derived FieldKind = "derived"
	// Identity is supplied by the caller at insert. Changing it is an error.
	Identity FieldKind = "identity"
	// Created is stamped by the store at insert, and immutable after.
	Created FieldKind = "created"
	// Auto is maintained by the store on every write, and not worth logging
	// because it changes whenever anything else does.
	Auto FieldKind = "auto"
)

const (
	columnTag = "db"
	kindTag   = "kind"
	formatTag = "format"
)

// formatJSON marks a TEXT column whose contents are themselves JSON — the
// schema's json_valid() columns. It changes only how the value crosses the
// JSON boundary: such a column is rendered as the structure it holds rather
// than as a quoted string, and accepts one on the way in. In the database and
// in the event log it stays text.
const formatJSON = "json"

// formatMarkdown marks a TEXT column written as prose rather than as a value —
// action.why, project.summary, the snooze reasons, a calendar note. Like
// formatJSON it changes nothing about storage: the column holds the source the
// author typed, the event log records that source, and `show -o json` returns
// it. Only the page renders.
//
// It also carries one input rule, enforced in checkProse: no raw HTML.
const formatMarkdown = "markdown"

var fieldFormats = map[string]bool{"": true, formatJSON: true, formatMarkdown: true}

var fieldKinds = map[string]FieldKind{
	string(Authored): Authored,
	string(Observed): Observed,
	string(Derived):  Derived,
	string(Identity): Identity,
	string(Created):  Created,
	string(Auto):     Auto,
}

// field is one column of a record.
type field struct {
	column string
	kind   FieldKind
	format string
	index  int
}

// fieldsOf reads the column metadata off a record's struct tags. Struct
// fields without a db tag are ignored; a field with no kind tag is Authored,
// since that is the common case and the safer default.
//
// r must be a pointer to a struct.
func fieldsOf(r Record) ([]field, error) {
	return fieldsOfStruct(r)
}

// Columns names the columns a record carries, in the order its struct
// declares them.
//
// Exported because output has to be able to name what it can show. A listing
// that offers to choose its fields needs that vocabulary before it has any
// rows to read it off — an empty result still has to say which names it would
// have accepted.
//
// Struct order rather than the alphabetical order MarshalRecord produces:
// that is the order the entity is written in, so identity comes first and the
// timestamps come last, which is also how it reads as a table.
func Columns(r any) ([]string, error) {
	fields, err := fieldsOfStruct(r)
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}
	return columns, nil
}

// fieldsOfStruct is fieldsOf without the Record constraint, so the reflection
// rules can be tested against shapes that are not valid records.
func fieldsOfStruct(r any) ([]field, error) {
	v := reflect.ValueOf(r)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("%T is not a pointer to a struct", r)
	}

	t := v.Elem().Type()
	fields := make([]field, 0, t.NumField())
	for i := range t.NumField() {
		structField := t.Field(i)
		column, ok := structField.Tag.Lookup(columnTag)
		if !ok || column == "" {
			continue
		}
		kind, err := kindOf(structField.Tag.Get(kindTag))
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name(), structField.Name, err)
		}
		format := structField.Tag.Get(formatTag)
		if !fieldFormats[format] {
			return nil, fmt.Errorf("%s.%s: unknown field format %q", t.Name(), structField.Name, format)
		}
		fields = append(fields, field{column: column, kind: kind, format: format, index: i})
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("%T has no %q-tagged fields", r, columnTag)
	}
	return fields, nil
}

func kindOf(tag string) (FieldKind, error) {
	if tag == "" {
		return Authored, nil
	}
	kind, ok := fieldKinds[tag]
	if !ok {
		return "", fmt.Errorf("unknown field kind %q", tag)
	}
	return kind, nil
}

// value returns the field's current value as an any, suitable for passing to
// database/sql as a query argument.
func (f field) value(r Record) any {
	return f.valueOf(r)
}

// valueOf is value without the Record constraint, for rows that have no
// entity behind them.
func (f field) valueOf(r any) any {
	return reflect.ValueOf(r).Elem().Field(f.index).Interface()
}

// pointer returns a pointer to the field, for Scan to write through.
func (f field) pointer(r Record) any {
	return f.pointerOf(r)
}

// pointerOf is pointer without the Record constraint.
func (f field) pointerOf(r any) any {
	return reflect.ValueOf(r).Elem().Field(f.index).Addr().Interface()
}

// renderValue converts a column value to the text stored in event.old_value
// and event.new_value.
//
// NULL renders as the empty string, and so does an empty string: the event
// columns are TEXT NOT NULL, so the log cannot distinguish the two. That is
// lossy by design rather than by accident — it keeps every log query a plain
// string comparison.
//
// Unsupported types are an error rather than a best-effort stringification,
// so a new column type fails loudly the first time it is written.
func renderValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case bool:
		return boolText(x), nil
	case sql.NullString:
		if !x.Valid {
			return "", nil
		}
		return x.String, nil
	case sql.NullInt64:
		if !x.Valid {
			return "", nil
		}
		return strconv.FormatInt(x.Int64, 10), nil
	case sql.NullBool:
		if !x.Valid {
			return "", nil
		}
		return boolText(x.Bool), nil
	default:
		return "", fmt.Errorf("no text rendering for %T", v)
	}
}

// boolText matches SQLite's integer booleans, which the schema's CHECK
// constraints spell as 0 and 1.
func boolText(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ColumnType is the shape of one column, for a caller that has to build a
// type system of its own over a record — a filter language, say.
//
// Coarse on purpose: SQLite has four storage classes worth caring about here,
// and a caller wanting more can read the struct itself. JSON says the column
// holds a JSON array rather than a scalar, which is a different kind of
// question to ask of it.
type ColumnType struct {
	Name string
	// Kind is "text", "integer", "boolean" or "real".
	Kind string
	// JSON says the column's text is itself JSON — the format:"json" columns.
	JSON bool
	// Nullable says the column is a sql.Null* type, so a value may be absent
	// rather than empty.
	Nullable bool
}

// ColumnTypes reports the columns a record carries, with enough about each to
// build a query language over it.
//
// Exported for the same reason Columns is: something outside the store has to
// be able to name and type what a record holds, without a second copy of the
// struct tags to drift from these.
func ColumnTypes(r any) ([]ColumnType, error) {
	fields, err := fieldsOfStruct(r)
	if err != nil {
		return nil, err
	}
	v := reflect.ValueOf(r).Elem().Type()

	types := make([]ColumnType, len(fields))
	for i, f := range fields {
		t := ColumnType{Name: f.column, JSON: f.format == formatJSON}
		t.Kind, t.Nullable = kindOfGoType(v.Field(f.index).Type)
		types[i] = t
	}
	return types, nil
}

// kindOfGoType maps a struct field's type to a storage kind.
func kindOfGoType(t reflect.Type) (kind string, nullable bool) {
	switch t {
	case reflect.TypeOf(sql.NullString{}):
		return "text", true
	case reflect.TypeOf(sql.NullInt64{}):
		return "integer", true
	case reflect.TypeOf(sql.NullBool{}):
		return "boolean", true
	case reflect.TypeOf(sql.NullFloat64{}):
		return "real", true
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean", false
	case reflect.Int, reflect.Int32, reflect.Int64:
		return "integer", false
	case reflect.Float32, reflect.Float64:
		return "real", false
	default:
		return "text", false
	}
}

// ColumnValues reads a record's columns into a map, with absent values left
// out entirely rather than zeroed.
//
// The distinction is the point: a NULL column is not an empty string, and a
// filter that could not tell them apart would answer differently from the same
// filter run as SQL.
func ColumnValues(r any) (map[string]any, error) {
	fields, err := fieldsOfStruct(r)
	if err != nil {
		return nil, err
	}
	v := reflect.ValueOf(r).Elem()

	values := make(map[string]any, len(fields))
	for _, f := range fields {
		value, ok := valueOfField(v.Field(f.index))
		if !ok {
			continue
		}
		if f.format == formatJSON {
			var list []string
			if text, isText := value.(string); isText && text != "" {
				if err := json.Unmarshal([]byte(text), &list); err != nil {
					// A column that does not hold what it says it holds is
					// not a reason to fail a listing: it reads as empty, and
					// the filter simply does not match it.
					list = nil
				}
			}
			values[f.column] = list
			continue
		}
		values[f.column] = value
	}
	return values, nil
}

// valueOfField unwraps a sql.Null*, reporting absence rather than a zero.
func valueOfField(v reflect.Value) (any, bool) {
	switch value := v.Interface().(type) {
	case sql.NullString:
		return value.String, value.Valid
	case sql.NullInt64:
		return value.Int64, value.Valid
	case sql.NullBool:
		return value.Bool, value.Valid
	case sql.NullFloat64:
		return value.Float64, value.Valid
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int32:
		return v.Int(), true
	default:
		return v.Interface(), true
	}
}
