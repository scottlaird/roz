package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// addProject creates one project and returns its id.
func addProject(t *testing.T, db, title string, extra ...string) string {
	t.Helper()
	args := append([]string{"project", "add", "--db", db, "--title", title}, extra...)
	out, err := runCLI(t, args...)
	if err != nil {
		t.Fatalf("project add returned error: %v", err)
	}
	return strings.TrimSpace(out)
}

func TestProjectShow(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title", "--priority", "1", "--design-ref", "docs/a.md")

	out, err := runCLI(t, "project", "show", "--db", db, id)
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	for _, want := range []string{"a title", "SL1", "priority", `["docs/a.md"]`} {
		if !strings.Contains(out, want) {
			t.Errorf("show output does not contain %q:\n%s", want, out)
		}
	}
	// An unset nullable reads as a dash rather than as an empty column.
	if !strings.Contains(out, "effort            -") {
		t.Errorf("show output does not mark effort absent:\n%s", out)
	}
}

func TestProjectShowJSON(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	out, err := runCLI(t, "project", "show", "--db", db, id, "-o", "json")
	if err != nil {
		t.Fatalf("project show -o json returned error: %v", err)
	}

	// One object, not an array — show returns a single record.
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not a JSON object: %v\n%s", err, out)
	}
	if object["id"] != id {
		t.Errorf("id = %#v, want %q", object["id"], id)
	}
}

func TestProjectShowMissing(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "project", "show", "--db", db, "SL404")
	if err == nil {
		t.Fatal("project show on a missing project returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "SL404") {
		t.Errorf("error = %v, want it to name the identifier", err)
	}
}

// TestProjectSnoozeAndWake covers the pair together, because the schema
// couples status and snooze_until with a CHECK.
func TestProjectSnoozeAndWake(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	out, err := runCLI(t, "project", "snooze", "--db", db, id,
		"--snooze-until", "2026-08-21", "--snooze-reason", "after oncall")
	if err != nil {
		t.Fatalf("project snooze returned error: %v", err)
	}
	for _, want := range []string{"status", "snoozed", "snooze_until", "2026-08-21"} {
		if !strings.Contains(out, want) {
			t.Errorf("snooze output does not mention %q:\n%s", want, out)
		}
	}

	if _, err := runCLI(t, "project", "wake", "--db", db, id); err != nil {
		t.Fatalf("project wake returned error: %v", err)
	}

	shown, err := runCLI(t, "project", "show", "--db", db, id, "-o", "json")
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(shown), &object); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if object["status"] != "active" {
		t.Errorf("status after wake = %#v, want active", object["status"])
	}
	if object["snooze_until"] != nil {
		t.Errorf("snooze_until after wake = %#v, want null", object["snooze_until"])
	}
}

func TestProjectWakeIntoAnotherStatus(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	if _, err := runCLI(t, "project", "snooze", "--db", db, id, "--snooze-until", "2026-08-21"); err != nil {
		t.Fatalf("project snooze returned error: %v", err)
	}
	if _, err := runCLI(t, "project", "wake", "--db", db, id, "--status", "blocked"); err != nil {
		t.Fatalf("project wake --status blocked returned error: %v", err)
	}

	out, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	if !strings.Contains(out, "blocked") {
		t.Errorf("project did not wake into blocked:\n%s", out)
	}
}

func TestProjectSnoozeExpiredIsFound(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "revisit on Thursday")

	if _, err := runCLI(t, "project", "snooze", "--db", db, id, "--snooze-until", "2000-01-01"); err != nil {
		t.Fatalf("project snooze returned error: %v", err)
	}

	out, err := runCLI(t, "project", "list", "--db", db, "--expired")
	if err != nil {
		t.Fatalf("project list --expired returned error: %v", err)
	}
	if !strings.Contains(out, id) {
		t.Errorf("--expired did not surface the past snooze:\n%s", out)
	}
}

func TestProjectSupersede(t *testing.T) {
	db := initDB(t)
	from := addProject(t, db, "the duplicate")
	into := addProject(t, db, "the survivor")

	if _, err := runCLI(t, "project", "supersede", "--db", db, "--from", from, "--into", into); err != nil {
		t.Fatalf("project supersede returned error: %v", err)
	}

	out, err := runCLI(t, "project", "show", "--db", db, from, "-o", "json")
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if object["status"] != "superseded" || object["superseded_by"] != into {
		t.Errorf("superseded project = %#v, want status superseded and superseded_by %s", object, into)
	}

	// The target is untouched, so old references to it still resolve.
	target, err := runCLI(t, "project", "show", "--db", db, into, "-o", "json")
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	var targetObject map[string]any
	if err := json.Unmarshal([]byte(target), &targetObject); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if targetObject["status"] != "active" {
		t.Errorf("target status = %#v, want it untouched", targetObject["status"])
	}
}

func TestNoteAndExceptionReachTheLog(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	if _, err := runCLI(t, "note", "--db", db, id, "worth remembering"); err != nil {
		t.Fatalf("note returned error: %v", err)
	}
	if _, err := runCLI(t, "exception", "--db", db,
		"--subject", id, "--kind", "unexpected_review_state", "--note", "needs a look"); err != nil {
		t.Fatalf("exception returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	for _, want := range []string{"note", "worth remembering", "unexpected_review_state", "needs a look"} {
		if !strings.Contains(out, want) {
			t.Errorf("log does not contain %q:\n%s", want, out)
		}
	}

	// The exception is reachable by severity alone, which is the point of
	// keeping severity apart from kind.
	filtered, err := runCLI(t, "watch", "--db", db, "--once", "--severity", "exception", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch --severity exception returned error: %v", err)
	}
	if got := len(nonEmptyLines(filtered)); got != 1 {
		t.Errorf("got %d exception events, want 1:\n%s", got, filtered)
	}
}

// TestSubjectResolutionUsesThePrefixRegistry checks an unknown prefix is
// reported against the prefixes this database actually issues, rather than
// against a hardcoded list.
func TestSubjectResolutionUsesThePrefixRegistry(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "note", "--db", db, "ZZ9", "nope")
	if err == nil {
		t.Fatal("note on an unknown prefix returned nil, want an error")
	}
	for _, want := range []string{"ZZ9", "project=SL", "action=NA"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

func TestVerbRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "snooze without a real date",
			args:    []string{"project", "snooze", "SL1", "--snooze-until", "next week"},
			wantErr: "not a date or timestamp",
		},
		{
			name:    "wake something that is not snoozed",
			args:    []string{"project", "wake", "SL1"},
			wantErr: "not snoozed",
		},
		{
			name:    "wake into snoozed",
			args:    []string{"project", "wake", "SL1", "--status", "snoozed"},
			wantErr: "would not wake anything",
		},
		{
			name:    "supersede itself",
			args:    []string{"project", "supersede", "--from", "SL1", "--into", "SL1"},
			wantErr: "cannot supersede itself",
		},
		{
			name:    "supersede into a missing project",
			args:    []string{"project", "supersede", "--from", "SL1", "--into", "SL999"},
			wantErr: "no such item: SL999",
		},
		{
			name:    "note on a missing project",
			args:    []string{"note", "SL404", "text"},
			wantErr: "no such item: SL404",
		},
		{
			name:    "sync actor on a write verb",
			args:    []string{"project", "snooze", "SL1", "--snooze-until", "2030-01-01", "--actor", "sync:jira"},
			wantErr: "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			addProject(t, db, "a title")

			args := append([]string{tt.args[0]}, tt.args[1:]...)
			args = append(args, "--db", db)

			_, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("%v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestUpdateLogsOneEventPerChangedField is the diff-and-emit machinery seen
// from the CLI: one snooze moves three columns and writes three events.
func TestUpdateLogsOneEventPerChangedField(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "a title")

	if _, err := runCLI(t, "project", "snooze", "--db", db, id,
		"--snooze-until", "2026-08-21", "--snooze-reason", "after oncall"); err != nil {
		t.Fatalf("project snooze returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once", "--kind", "changed", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d changed events, want one per column:\n%s", len(lines), out)
	}
	for _, want := range []string{"status", "snooze_until", "snooze_reason"} {
		if !strings.Contains(out, want) {
			t.Errorf("no event for %q:\n%s", want, out)
		}
	}
}
