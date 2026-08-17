package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ApplyJSON sets fields on r from a JSON object keyed by column name.
//
// Only authored columns may be set. Naming an observed one is an error rather
// than a silent skip: it is the same confusion Tx.Update guards against, and
// catching it here means the caller finds out before a transaction opens.
// Identity, created and auto columns are equally refused — those belong to
// the store.
//
// A JSON null clears a nullable column. An unknown column name is an error,
// so a typo does not quietly do nothing.
func ApplyJSON(r Record, data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}

	fields, err := fieldsOf(r)
	if err != nil {
		return err
	}
	byColumn := make(map[string]field, len(fields))
	for _, f := range fields {
		byColumn[f.column] = f
	}

	for _, column := range sortedKeys(raw) {
		f, ok := byColumn[column]
		if !ok {
			return fmt.Errorf("%s has no column %q; settable columns are %s",
				r.table(), column, strings.Join(settableColumns(fields), ", "))
		}
		if f.kind != Authored {
			return fmt.Errorf("%s.%s is %s, not authored, so it cannot be set here",
				r.table(), column, f.kind)
		}
		if verb, guarded := guardedColumns[r.table()+"."+column]; guarded {
			return fmt.Errorf("%s.%s is what `%s` is for, and that checks things this cannot; "+
				"set it there instead", r.table(), column, verb)
		}
		if err := setField(r, f, raw[column]); err != nil {
			return fmt.Errorf("%s.%s: %w", r.table(), column, err)
		}
	}
	return nil
}

// MarshalRecord renders any db-tagged struct as a JSON object keyed by column
// name. It takes any rather than Record because the event log has rows but no
// loadable entity behind them.
//
// Every column is included, observed and store-owned ones as well: this is
// output, and the authored/observed split is about who may write.
//
// NULL becomes null rather than an empty string — unlike the event log, which
// cannot tell them apart — and a format:"json" column becomes the structure
// it holds rather than a quoted string. Keys are ordered by encoding/json,
// which sorts map keys, so output is alphabetical and stable.
func MarshalRecord(r any) ([]byte, error) {
	object, err := recordObject(r)
	if err != nil {
		return nil, err
	}
	return json.Marshal(object)
}

// MarshalRecordWith encodes a record together with values that are not
// columns of its table: what Tx.MarshalRecord has loaded from its relations,
// or a property of the database that belongs beside the row without living in
// it — the identifier prefixes on `config show`, which are the sequence
// table's.
//
// The extras are merged after the columns and cannot collide with one: a
// relation named after a column would be a mistake worth making loudly, and
// the entity declaring both is the place to notice it.
func MarshalRecordWith(r any, extra map[string]any) ([]byte, error) {
	object, err := recordObject(r)
	if err != nil {
		return nil, err
	}
	for name, value := range extra {
		object[name] = value
	}
	return json.Marshal(object)
}

// MarshalRecords renders a slice of records as a JSON array. An empty slice
// is [], never null, so a consumer can iterate without a nil check.
func MarshalRecords[T any](records []T) ([]byte, error) {
	objects := make([]map[string]any, 0, len(records))
	for _, r := range records {
		object, err := recordObject(r)
		if err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return json.Marshal(objects)
}

func recordObject(r any) (map[string]any, error) {
	fields, err := fieldsOfStruct(r)
	if err != nil {
		return nil, err
	}

	object := make(map[string]any, len(fields))
	for _, f := range fields {
		value, err := jsonValue(f, f.valueOf(r))
		if err != nil {
			return nil, fmt.Errorf("%T.%s: %w", r, f.column, err)
		}
		object[f.column] = value
	}
	if extra, ok := r.(jsonExtra); ok {
		for key, value := range extra.extraJSON() {
			object[key] = value
		}
	}
	return object, nil
}

// jsonExtra lets a record contribute values that are not columns of its own
// table. A pipeline's steps are the case: they live in a child table, and a
// pipeline printed without them says nothing.
type jsonExtra interface {
	extraJSON() map[string]any
}

// jsonValue converts a column value to something encoding/json renders the
// way a consumer expects.
func jsonValue(f field, v any) (any, error) {
	if f.format == formatJSON {
		return embeddedJSON(v)
	}

	switch x := v.(type) {
	case string:
		return x, nil
	case int64:
		return x, nil
	case bool:
		return x, nil
	case sql.NullString:
		if !x.Valid {
			return nil, nil
		}
		return x.String, nil
	case sql.NullInt64:
		if !x.Valid {
			return nil, nil
		}
		return x.Int64, nil
	case sql.NullBool:
		if !x.Valid {
			return nil, nil
		}
		return x.Bool, nil
	default:
		return nil, fmt.Errorf("no JSON rendering for %T", v)
	}
}

// embeddedJSON passes a JSON-valued text column through unquoted. The schema
// guarantees validity with json_valid(), but a record built in memory and not
// yet inserted has not been through that, so it is checked here rather than
// producing malformed output.
func embeddedJSON(v any) (any, error) {
	text, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf(`format:"json" needs a string column, not %T`, v)
	}
	if text == "" {
		return nil, nil
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("column holds invalid JSON: %q", text)
	}
	return json.RawMessage(text), nil
}

// setField decodes one JSON value into a column, rejecting types the store
// has no rendering for rather than guessing.
func setField(r Record, f field, raw json.RawMessage) error {
	target := reflect.ValueOf(r).Elem().Field(f.index)

	if f.format == formatJSON {
		return setEmbeddedJSON(target, raw)
	}

	switch target.Interface().(type) {
	case string:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("expected a string: %w", err)
		}
		target.SetString(s)
	case int64:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return fmt.Errorf("expected a number: %w", err)
		}
		target.SetInt(n)
	case sql.NullString:
		var s *string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("expected a string or null: %w", err)
		}
		value := sql.NullString{}
		if s != nil {
			value = sql.NullString{String: *s, Valid: true}
		}
		target.Set(reflect.ValueOf(value))
	case sql.NullInt64:
		var n *int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return fmt.Errorf("expected a number or null: %w", err)
		}
		value := sql.NullInt64{}
		if n != nil {
			value = sql.NullInt64{Int64: *n, Valid: true}
		}
		target.Set(reflect.ValueOf(value))
	default:
		return fmt.Errorf("no JSON decoding for %s", target.Type())
	}
	return nil
}

// setEmbeddedJSON stores a JSON value as compact text. The value has already
// been parsed by the enclosing Unmarshal, so it is known to be valid.
func setEmbeddedJSON(target reflect.Value, raw json.RawMessage) error {
	if target.Kind() != reflect.String {
		return fmt.Errorf(`format:"json" needs a string column, not %s`, target.Type())
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return err
	}
	target.SetString(compact.String())
	return nil
}

func settableColumns(fields []field) []string {
	var columns []string
	for _, f := range fields {
		if f.kind == Authored {
			columns = append(columns, f.column)
		}
	}
	return columns
}

// sortedKeys makes error reporting deterministic when several keys are wrong.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// guardedColumns are the columns a purpose-built command exists to write,
// keyed table.column, valued with the command to use instead.
//
// #107 asked whether --json should refuse these or leave them, and refusing is
// what was chosen. The damage from leaving them was bounded — the foreign key
// still catches a target that does not exist, so the cost was a rawer error
// rather than a bad row — but the two paths did not agree about what checking
// means, and the quiet half is the problem: `project set --json
// '{"superseded_by":"ROZ94"}'` skipped the target-existence check that
// `project supersede` performs, and also skipped writing the other end.
//
// A list rather than a rule, because there is no property of a column that
// says a command guards it. That means it is a second place to keep in step
// with the verbs, which was the argument for leaving it alone; the answer is
// TestEveryGuardedColumnExists, which fails if a name here stops being a
// column, and a test naming each command beside the column it guards.
//
// It is deliberately not every column a command can touch. `project set
// --status` writes status too, and so does this; what belongs here is the
// column whose dedicated command does something *besides* the write —
// checking a target exists, recording the other end of a pair, or coupling two
// columns that must move together.
var guardedColumns = map[string]string{
	// Checks the target exists, and records both ends of the pair.
	"project.superseded_by": "roz project supersede",
	// Coupled to status, and the pair must move together: a date with no
	// snooze is invisible, and a snooze with no date never wakes.
	"project.snooze_until": "roz project snooze",
	// Coupled to state, and closing cascades — it frees dependents, unhides
	// what was folded behind, and stands down chases.
	"action.closed_at":     "roz action close",
	"action.closed_reason": "roz action close",
	"action.snooze_until":  "roz action snooze",
	// Checks the two are different actions and that the target exists, and
	// then decides whether the action is still in the queue.
	"action.hidden_behind": "roz action hide-behind",
}
