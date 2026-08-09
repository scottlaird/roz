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
		if err := setField(r, f, raw[column]); err != nil {
			return fmt.Errorf("%s.%s: %w", r.table(), column, err)
		}
	}
	return nil
}

// MarshalRecord renders a record as a JSON object keyed by column name.
//
// Every column is included, observed and store-owned ones as well: this is
// output, and the authored/observed split is about who may write.
//
// NULL becomes null rather than an empty string — unlike the event log, which
// cannot tell them apart — and a format:"json" column becomes the structure
// it holds rather than a quoted string. Keys are ordered by encoding/json,
// which sorts map keys, so output is alphabetical and stable.
func MarshalRecord(r Record) ([]byte, error) {
	object, err := recordObject(r)
	if err != nil {
		return nil, err
	}
	return json.Marshal(object)
}

// MarshalRecords renders a slice of records as a JSON array. An empty slice
// is [], never null, so a consumer can iterate without a nil check.
func MarshalRecords[T Record](records []T) ([]byte, error) {
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

func recordObject(r Record) (map[string]any, error) {
	fields, err := fieldsOf(r)
	if err != nil {
		return nil, err
	}

	object := make(map[string]any, len(fields))
	for _, f := range fields {
		value, err := jsonValue(f, f.value(r))
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", r.table(), f.column, err)
		}
		object[f.column] = value
	}
	return object, nil
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
