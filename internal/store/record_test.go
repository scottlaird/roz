package store

import (
	"database/sql"
	"strings"
	"testing"
)

// sample exercises every field kind and every renderable column type. Using a
// synthetic record keeps the diff tests independent of the real schema.
type sample struct {
	ID      string         `db:"id" kind:"identity"`
	Title   string         `db:"title"`
	Count   sql.NullInt64  `db:"count"`
	Note    sql.NullString `db:"note"`
	Fetched sql.NullString `db:"fetched" kind:"observed"`
	Frozen  bool           `db:"frozen" kind:"derived"`
	Touched string         `db:"touched" kind:"auto"`
	Born    string         `db:"born" kind:"identity"`
	Ignored string
}

func (s *sample) table() string       { return "sample" }
func (s *sample) subjectType() string { return "sample" }
func (s *sample) subjectID() string   { return s.ID }

func TestFieldsOf(t *testing.T) {
	fields, err := fieldsOf(&sample{})
	if err != nil {
		t.Fatalf("fieldsOf() returned error: %v", err)
	}

	want := []field{
		{column: "id", kind: Identity},
		{column: "title", kind: Authored},
		{column: "count", kind: Authored},
		{column: "note", kind: Authored},
		{column: "fetched", kind: Observed},
		{column: "frozen", kind: Derived},
		{column: "touched", kind: Auto},
		{column: "born", kind: Identity},
	}
	if len(fields) != len(want) {
		t.Fatalf("fieldsOf() returned %d fields, want %d", len(fields), len(want))
	}
	for i, got := range fields {
		if got.column != want[i].column || got.kind != want[i].kind {
			t.Errorf("field %d = {%s %s}, want {%s %s}",
				i, got.column, got.kind, want[i].column, want[i].kind)
		}
	}
}

func TestFieldsOfRejectsBadInput(t *testing.T) {
	type unknownKind struct {
		ID string `db:"id" kind:"invented"`
	}
	type untagged struct {
		ID string
	}

	tests := []struct {
		name   string
		record any
	}{
		{name: "not a pointer", record: sample{}},
		{name: "unknown kind", record: &unknownKind{}},
		{name: "no tagged fields", record: &untagged{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := fieldsOfStruct(tt.record); err == nil {
				t.Error("fieldsOfStruct() returned nil, want an error")
			}
		})
	}
}

func TestRenderValue(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		want    string
		wantErr bool
	}{
		{name: "string", value: "hello", want: "hello"},
		{name: "empty string", value: "", want: ""},
		{name: "int64", value: int64(42), want: "42"},
		{name: "true", value: true, want: "1"},
		{name: "false", value: false, want: "0"},
		{name: "valid NullString", value: sql.NullString{String: "x", Valid: true}, want: "x"},
		{name: "NULL NullString", value: sql.NullString{}, want: ""},
		{name: "valid NullInt64", value: sql.NullInt64{Int64: 7, Valid: true}, want: "7"},
		{name: "NULL NullInt64", value: sql.NullInt64{}, want: ""},
		{name: "unsupported", value: 3.5, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderValue(tt.value)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("renderValue(%v) error = %v, want error %v", tt.value, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("renderValue(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestDiff(t *testing.T) {
	base := func() *sample {
		return &sample{ID: "S1", Title: "before", Touched: "t0"}
	}

	tests := []struct {
		name    string
		mutate  func(*sample)
		want    []Change
		wantErr bool
	}{
		{
			name:   "no change",
			mutate: func(*sample) {},
			want:   nil,
		},
		{
			name:   "authored column",
			mutate: func(s *sample) { s.Title = "after" },
			want:   []Change{{Column: "title", Kind: Authored, Old: "before", New: "after"}},
		},
		{
			name:   "observed column",
			mutate: func(s *sample) { s.Fetched = sql.NullString{String: "now", Valid: true} },
			want:   []Change{{Column: "fetched", Kind: Observed, Old: "", New: "now"}},
		},
		{
			name:   "null to value",
			mutate: func(s *sample) { s.Count = sql.NullInt64{Int64: 3, Valid: true} },
			want:   []Change{{Column: "count", Kind: Authored, Old: "", New: "3"}},
		},
		{
			name:   "derived column is not a change",
			mutate: func(s *sample) { s.Frozen = true },
			want:   nil,
		},
		{
			name:   "auto column is not a change",
			mutate: func(s *sample) { s.Touched = "t1" },
			want:   nil,
		},
		{
			name:   "untagged field is not a change",
			mutate: func(s *sample) { s.Ignored = "x" },
			want:   nil,
		},
		{
			name:    "identity column is an error",
			mutate:  func(s *sample) { s.Born = "later" },
			wantErr: true,
		},
		{
			name: "several columns at once",
			mutate: func(s *sample) {
				s.Title = "after"
				s.Note = sql.NullString{String: "n", Valid: true}
			},
			want: []Change{
				{Column: "title", Kind: Authored, Old: "before", New: "after"},
				{Column: "note", Kind: Authored, Old: "", New: "n"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after := base(), base()
			tt.mutate(after)

			got, err := diff(before, after)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("diff() error = %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !changesEqual(got, tt.want) {
				t.Errorf("diff() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiffRejectsMismatchedRecords(t *testing.T) {
	a := &sample{ID: "S1"}
	b := &sample{ID: "S2"}
	if _, err := diff(a, b); err == nil {
		t.Error("diff() across different ids returned nil, want an error")
	}
}

func changesEqual(got, want []Change) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestChangeString(t *testing.T) {
	c := Change{Column: "title", Old: "a", New: "b"}
	if got := c.String(); !strings.Contains(got, "title") {
		t.Errorf("Change.String() = %q, want it to mention the column", got)
	}
}
