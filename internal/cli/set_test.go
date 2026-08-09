package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// showJSON reads one project back as a decoded object.
func showJSON(t *testing.T, db, id string) map[string]any {
	t.Helper()
	out, err := runCLI(t, "project", "show", "--db", db, id, "-o", "json")
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return object
}

// TestSetEveryFieldFlag checks each flag reaches the column it names.
//
// This exists because --title was registered on `set` but never applied, so
// the flag was silently ignored. A per-flag table is the only thing that
// catches the next one of those.
func TestSetEveryFieldFlag(t *testing.T) {
	tests := []struct {
		flag   string
		args   []string
		column string
		want   any
	}{
		{flag: flagTitle, args: []string{"--title", "changed"}, column: "title", want: "changed"},
		{flag: flagSummary, args: []string{"--summary", "a summary"}, column: "summary", want: "a summary"},
		{flag: flagStatus, args: []string{"--status", "blocked"}, column: "status", want: "blocked"},
		{flag: flagPriority, args: []string{"--priority", "3"}, column: "priority", want: float64(3)},
		{flag: flagEffort, args: []string{"--effort", "hours"}, column: "effort", want: "hours"},
		{flag: flagSnoozeReason, args: []string{"--snooze-reason", "later"}, column: "snooze_reason", want: "later"},
		{flag: flagJiraKey, args: []string{"--jira-key", "CDSS-1"}, column: "jira_key", want: "CDSS-1"},
	}

	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			db := initDB(t)
			id := addProject(t, db, "a title")

			args := append([]string{"project", "set", "--db", db, id}, tt.args...)
			if _, err := runCLI(t, args...); err != nil {
				t.Fatalf("project set %v returned error: %v", tt.args, err)
			}

			if got := showJSON(t, db, id)[tt.column]; got != tt.want {
				t.Errorf("%s = %#v, want %#v", tt.column, got, tt.want)
			}
		})
	}
}

// TestSetEveryFieldFlagIsApplied guards the registration and the applier
// against drifting apart: every flag `set` advertises must change something.
func TestSetEveryFieldFlagIsApplied(t *testing.T) {
	// snooze-until is excluded: the schema couples it to status, so it cannot
	// move on its own. TestSetSnoozePairTogether covers it.
	skip := map[string]bool{flagSnoozeUntil: true}

	values := map[string]string{
		flagTitle:        "t",
		flagSummary:      "s",
		flagStatus:       "blocked",
		flagPriority:     "2",
		flagEffort:       "days",
		flagSnoozeReason: "r",
		flagDesignRef:    "docs/a.md",
		flagJiraKey:      "K-1",
	}

	for _, flag := range projectFieldFlags {
		if skip[flag] {
			continue
		}
		value, ok := values[flag]
		if !ok {
			t.Fatalf("no test value for flag --%s; add one when adding the flag", flag)
		}

		t.Run(flag, func(t *testing.T) {
			db := initDB(t)
			id := addProject(t, db, "untouched")

			out, err := runCLI(t, "project", "set", "--db", db, id, "--"+flag, value)
			if err != nil {
				t.Fatalf("project set --%s returned error: %v", flag, err)
			}
			if strings.Contains(out, "unchanged") {
				t.Errorf("--%s changed nothing; the flag is registered but not applied", flag)
			}
		})
	}
}

func TestSetSnoozePairTogether(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	if _, err := runCLI(t, "project", "set", "--db", db, id,
		"--status", "snoozed", "--snooze-until", "2026-08-21"); err != nil {
		t.Fatalf("project set returned error: %v", err)
	}

	object := showJSON(t, db, id)
	if object["status"] != "snoozed" || object["snooze_until"] != "2026-08-21" {
		t.Errorf("project = %#v, want snoozed until 2026-08-21", object)
	}
}

func TestSetClearsNullableColumns(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title", "--jira-key", "CDSS-1744", "--priority", "2")

	// An empty value clears a nullable string.
	if _, err := runCLI(t, "project", "set", "--db", db, id, "--jira-key", ""); err != nil {
		t.Fatalf("project set --jira-key '' returned error: %v", err)
	}
	// A number needs JSON null, since an empty flag value is not a number.
	if _, err := runCLI(t, "project", "set", "--db", db, id, "--json", `{"priority":null}`); err != nil {
		t.Fatalf("project set --json returned error: %v", err)
	}

	object := showJSON(t, db, id)
	if object["jira_key"] != nil {
		t.Errorf("jira_key = %#v, want null", object["jira_key"])
	}
	if object["priority"] != nil {
		t.Errorf("priority = %#v, want null", object["priority"])
	}
}

func TestSetFlagOverridesJSON(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	if _, err := runCLI(t, "project", "set", "--db", db, id,
		"--json", `{"summary":"from json","effort":"days"}`, "--effort", "hours"); err != nil {
		t.Fatalf("project set returned error: %v", err)
	}

	object := showJSON(t, db, id)
	if object["summary"] != "from json" {
		t.Errorf("summary = %#v, want the JSON value", object["summary"])
	}
	if object["effort"] != "hours" {
		t.Errorf("effort = %#v, want the flag to win", object["effort"])
	}
}

// TestSetNoOpWritesNothing checks that setting a value it already holds
// leaves no event behind.
func TestSetNoOpWritesNothing(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	out, err := runCLI(t, "project", "set", "--db", db, id, "--title", "a title")
	if err != nil {
		t.Fatalf("project set returned error: %v", err)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("set to the same value reported %q, want unchanged", out)
	}

	log, err := runCLI(t, "watch", "--db", db, "--once", "--kind", "changed", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if got := len(nonEmptyLines(log)); got != 0 {
		t.Errorf("a no-op set wrote %d events, want none:\n%s", got, log)
	}
}

func TestSetLogsOneEventPerColumn(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	if _, err := runCLI(t, "project", "set", "--db", db, id,
		"--title", "changed", "--priority", "2", "--effort", "days"); err != nil {
		t.Fatalf("project set returned error: %v", err)
	}

	log, err := runCLI(t, "watch", "--db", db, "--once", "--kind", "changed", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if got := len(nonEmptyLines(log)); got != 3 {
		t.Errorf("got %d events, want one per changed column:\n%s", got, log)
	}
}

func TestSetRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "nothing given",
			args:    nil,
			wantErr: "nothing to set",
		},
		{
			name:    "status snoozed with no date",
			args:    []string{"--status", "snoozed"},
			wantErr: "needs a date",
		},
		{
			name:    "date with no snoozed status",
			args:    []string{"--snooze-until", "2026-08-21"},
			wantErr: "needs status snoozed",
		},
		{
			name:    "observed column",
			args:    []string{"--json", `{"jira_status":"Done"}`},
			wantErr: "observed",
		},
		{
			name:    "identity column",
			args:    []string{"--json", `{"id":"SL99"}`},
			wantErr: "identity",
		},
		{
			name:    "sync actor",
			args:    []string{"--title", "x", "--actor", "sync:jira"},
			wantErr: "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			id := addProject(t, db, "a title")

			args := append([]string{"project", "set", "--db", db, id}, tt.args...)
			_, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("project set %v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSetMissingProject(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "project", "set", "--db", db, "SL404", "--title", "x")
	if err == nil {
		t.Fatal("project set on a missing project returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "SL404") {
		t.Errorf("error = %v, want it to name the identifier", err)
	}
}
