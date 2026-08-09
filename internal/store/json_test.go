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
			json: `{"jira_key":"CDSS-1744"}`,
			want: func(p *Project) bool { return p.JiraKey.Valid && p.JiraKey.String == "CDSS-1744" },
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
			json:    `{"jira_status":"Done"}`,
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
	if strings.Contains(err.Error(), "jira_status") {
		t.Errorf("error %v offers jira_status, which is observed", err)
	}
}
