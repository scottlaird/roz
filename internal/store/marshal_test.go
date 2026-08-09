package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// decode unmarshals into a generic map so tests assert on JSON shape rather
// than on byte-for-byte output.
func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, data)
	}
	return object
}

func TestMarshalRecord(t *testing.T) {
	p := &Project{
		ID:         "SL1",
		Kind:       "SL",
		N:          1,
		Title:      "a title",
		Status:     ProjectActive,
		Priority:   sql.NullInt64{Int64: 2, Valid: true},
		DesignRefs: `["docs/a.md","docs/b.md"]`,
	}

	data, err := MarshalRecord(p)
	if err != nil {
		t.Fatalf("MarshalRecord() returned error: %v", err)
	}
	got := decode(t, data)

	tests := []struct {
		column string
		want   any
	}{
		{"id", "SL1"},
		{"title", "a title"},
		{"status", "active"},
		{"n", float64(1)},        // JSON numbers decode as float64
		{"priority", float64(2)}, // a set nullable renders as a number
		{"effort", nil},          // an unset nullable renders as null
		{"jira_status", nil},     // observed columns are included in output
	}
	for _, tt := range tests {
		if got[tt.column] != tt.want {
			t.Errorf("%s = %#v, want %#v", tt.column, got[tt.column], tt.want)
		}
	}

	// A format:"json" column is a structure, not a quoted string.
	refs, ok := got["design_refs"].([]any)
	if !ok {
		t.Fatalf("design_refs = %#v, want an array", got["design_refs"])
	}
	if len(refs) != 2 || refs[0] != "docs/a.md" {
		t.Errorf("design_refs = %#v, want the two paths", refs)
	}
}

// TestMarshalRecordCoversEveryColumn guards against a column being added to
// the struct and quietly missing from output.
func TestMarshalRecordCoversEveryColumn(t *testing.T) {
	fields, err := fieldsOf(&Project{})
	if err != nil {
		t.Fatalf("fieldsOf() returned error: %v", err)
	}

	data, err := MarshalRecord(&Project{DesignRefs: "[]"})
	if err != nil {
		t.Fatalf("MarshalRecord() returned error: %v", err)
	}
	got := decode(t, data)

	if len(got) != len(fields) {
		t.Errorf("output has %d keys, want %d", len(got), len(fields))
	}
	for _, f := range fields {
		if _, ok := got[f.column]; !ok {
			t.Errorf("output is missing column %q", f.column)
		}
	}
}

func TestMarshalRecords(t *testing.T) {
	projects := []*Project{
		{ID: "SL1", DesignRefs: "[]"},
		{ID: "SL2", DesignRefs: "[]"},
	}

	data, err := MarshalRecords(projects)
	if err != nil {
		t.Fatalf("MarshalRecords() returned error: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(got) != 2 || got[0]["id"] != "SL1" || got[1]["id"] != "SL2" {
		t.Errorf("MarshalRecords() = %s, want SL1 then SL2", data)
	}
}

// TestMarshalRecordsEmpty pins [] rather than null, so consumers can iterate
// without a nil check.
func TestMarshalRecordsEmpty(t *testing.T) {
	data, err := MarshalRecords([]*Project{})
	if err != nil {
		t.Fatalf("MarshalRecords() returned error: %v", err)
	}
	if got, want := string(data), "[]"; got != want {
		t.Errorf("MarshalRecords(none) = %s, want %s", got, want)
	}

	data, err = MarshalRecords[*Project](nil)
	if err != nil {
		t.Fatalf("MarshalRecords(nil) returned error: %v", err)
	}
	if got, want := string(data), "[]"; got != want {
		t.Errorf("MarshalRecords(nil) = %s, want %s", got, want)
	}
}

func TestMarshalRecordRejectsInvalidEmbeddedJSON(t *testing.T) {
	p := &Project{ID: "SL1", DesignRefs: "not json"}

	if _, err := MarshalRecord(p); err == nil {
		t.Error("MarshalRecord() with a malformed JSON column returned nil, want an error")
	}
}

func TestApplyJSONToEmbeddedColumn(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "array", input: `{"design_refs":["a.md","b.md"]}`, want: `["a.md","b.md"]`},
		{name: "empty array", input: `{"design_refs":[]}`, want: `[]`},
		{name: "whitespace is compacted", input: `{"design_refs": [ "a.md" ]}`, want: `["a.md"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProject("t")
			if err := ApplyJSON(p, []byte(tt.input)); err != nil {
				t.Fatalf("ApplyJSON() returned error: %v", err)
			}
			if p.DesignRefs != tt.want {
				t.Errorf("design_refs = %q, want %q", p.DesignRefs, tt.want)
			}
		})
	}
}

// TestEmbeddedJSONRoundTrips checks the two directions agree.
func TestEmbeddedJSONRoundTrips(t *testing.T) {
	p := NewProject("t")
	if err := ApplyJSON(p, []byte(`{"design_refs":["docs/one.md","docs/two.md"]}`)); err != nil {
		t.Fatalf("ApplyJSON() returned error: %v", err)
	}

	data, err := MarshalRecord(p)
	if err != nil {
		t.Fatalf("MarshalRecord() returned error: %v", err)
	}
	if !strings.Contains(string(data), `"design_refs":["docs/one.md","docs/two.md"]`) {
		t.Errorf("round trip lost the structure: %s", data)
	}
}

func TestFieldsOfRejectsUnknownFormat(t *testing.T) {
	type bad struct {
		ID string `db:"id" format:"yaml"`
	}
	if _, err := fieldsOfStruct(&bad{}); err == nil {
		t.Error("fieldsOfStruct() accepted an unknown format, want an error")
	}
}
