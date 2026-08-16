package store

import (
	"database/sql"
	"strings"
	"testing"
)

func TestApplyJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    func(*Project) bool
		wantErr string
	}{
		{
			name: "string column",
			json: `{"summary":"a summary"}`,
			want: func(p *Project) bool { return p.Summary == "a summary" },
		},
		{
			name: "nullable int column",
			json: `{"priority":2}`,
			want: func(p *Project) bool { return p.Priority.Valid && p.Priority.Int64 == 2 },
		},
		{
			name: "nullable string column",
			json: `{"effort":"weeks"}`,
			want: func(p *Project) bool { return p.Effort.Valid && p.Effort.String == "weeks" },
		},
		{
			name: "null clears a nullable column",
			json: `{"priority":null}`,
			want: func(p *Project) bool { return !p.Priority.Valid },
		},
		{
			name: "several columns",
			json: `{"summary":"s","status":"blocked","effort":"hours"}`,
			want: func(p *Project) bool {
				return p.Summary == "s" && p.Status == "blocked" &&
					p.Effort.Valid && p.Effort.String == "hours"
			},
		},
		{
			name:    "observed column is refused",
			json:    `{"last_verified_at":"2026-08-10T00:00:00.000Z"}`,
			wantErr: "observed",
		},
		{
			name:    "identity column is refused",
			json:    `{"id":"SL99"}`,
			wantErr: "identity",
		},
		{
			name:    "created column is refused",
			json:    `{"created_at":"2020-01-01"}`,
			wantErr: "created",
		},
		{
			name:    "auto column is refused",
			json:    `{"updated_at":"2020-01-01"}`,
			wantErr: "auto",
		},
		{
			name:    "unknown column is refused",
			json:    `{"titel":"typo"}`,
			wantErr: `no column "titel"`,
		},
		{
			name:    "wrong type is refused",
			json:    `{"priority":"high"}`,
			wantErr: "expected a number",
		},
		{
			name:    "malformed JSON",
			json:    `{`,
			wantErr: "parsing JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Project{ID: "SL1", Priority: sql.NullInt64{Int64: 9, Valid: true}}

			err := ApplyJSON(p, []byte(tt.json))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ApplyJSON(%s) returned nil, want an error containing %q", tt.json, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ApplyJSON(%s) error = %v, want it to contain %q", tt.json, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyJSON(%s) returned error: %v", tt.json, err)
			}
			if !tt.want(p) {
				t.Errorf("ApplyJSON(%s) left %+v", tt.json, p)
			}
		})
	}
}

// TestApplyJSONListsSettableColumns checks the error is actionable rather
// than just correct.
func TestApplyJSONListsSettableColumns(t *testing.T) {
	err := ApplyJSON(&Project{}, []byte(`{"nope":1}`))
	if err == nil {
		t.Fatal("ApplyJSON() returned nil, want an error")
	}
	for _, column := range []string{"title", "summary", "priority"} {
		if !strings.Contains(err.Error(), column) {
			t.Errorf("error %v does not mention the settable column %q", err, column)
		}
	}
	if strings.Contains(err.Error(), "last_verified_at") {
		t.Errorf("error %v offers last_verified_at, which is observed", err)
	}
}

// TestJSONRefusesAColumnACommandGuards is #107, settled in favour of refusing.
//
// The damage from allowing it was bounded — the foreign key still caught a
// target that did not exist — but the two paths disagreed about what checking
// means, and the quiet half is what matters: `--json` skipped the check *and*
// skipped writing the other end of the pair.
func TestJSONRefusesAColumnACommandGuards(t *testing.T) {
	tests := []struct {
		name   string
		record Record
		json   string
		verb   string
	}{
		{"superseding by hand", &Project{}, `{"superseded_by":"SL94"}`, "project supersede"},
		{"snoozing by hand", &Project{}, `{"snooze_until":"2026-09-01"}`, "project snooze"},
		{"closing by hand", &Action{}, `{"closed_at":"2026-09-01T00:00:00.000Z"}`, "action close"},
		{"hiding by hand", &Action{}, `{"hidden_behind":"NA7"}`, "action hide-behind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ApplyJSON(tt.record, []byte(tt.json))
			if err == nil {
				t.Fatalf("ApplyJSON(%s) was accepted, want a refusal", tt.json)
			}
			// The error has to name the command, or it tells somebody they
			// cannot do the thing without telling them how to.
			if !strings.Contains(err.Error(), tt.verb) {
				t.Errorf("error = %v, want it to name `%s`", err, tt.verb)
			}
		})
	}
}

// TestJSONStillWritesOrdinaryColumns. The escape hatch is the point of --json;
// this narrows it rather than closing it.
func TestJSONStillWritesOrdinaryColumns(t *testing.T) {
	p := &Project{}
	if err := ApplyJSON(p, []byte(`{"title":"Split the nodepool","priority":2,"status":"active"}`)); err != nil {
		t.Fatalf("ApplyJSON() returned error: %v", err)
	}
	if p.Title != "Split the nodepool" || p.Priority.Int64 != 2 || p.Status != "active" {
		t.Errorf("applied %+v, want the three columns set", p)
	}
}

// TestEveryGuardedColumnExists is the cost of keeping this as a list: a name
// that stops being a column would silently guard nothing.
func TestEveryGuardedColumnExists(t *testing.T) {
	tables := map[string]Record{"project": &Project{}, "action": &Action{}}

	for key, verb := range guardedColumns {
		table, column, ok := strings.Cut(key, ".")
		if !ok {
			t.Errorf("%q is not table.column", key)
			continue
		}
		record, known := tables[table]
		if !known {
			t.Errorf("%q names table %q, which this test does not know", key, table)
			continue
		}
		fields, err := fieldsOf(record)
		if err != nil {
			t.Fatalf("fieldsOf(%s) returned error: %v", table, err)
		}
		var found bool
		for _, f := range fields {
			if f.column == column {
				found = true
				if f.kind != Authored {
					t.Errorf("%q is %s, so --json already refuses it and this entry is dead",
						key, f.kind)
				}
			}
		}
		if !found {
			t.Errorf("%q is not a column of %s", key, table)
		}
		if !strings.HasPrefix(verb, "roz ") {
			t.Errorf("%q names %q, which is not a command anybody can run", key, verb)
		}
	}
}
