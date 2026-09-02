package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runCLIWithInput is runCLI with something on stdin, which --feed - reads.
func runCLIWithInput(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)

	err := root.Execute()
	return out.String(), err
}

// issueJSON reads one Jira issue back as an object.
func issueJSON(t *testing.T, db, key string) map[string]any {
	t.Helper()

	out, err := runCLI(t, "issue", "show", "--db", db, key, "-o", "json")
	if err != nil {
		t.Fatalf("issue show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return object
}

// jiraProject adds a project carrying a Jira key.
func jiraProject(t *testing.T, db, title, key string) string {
	t.Helper()
	return addProject(t, db, title, "--issue", key)
}

func TestProjectJira(t *testing.T) {
	db := initDB(t)
	id := jiraProject(t, db, "Split the nodepool", "CDSS-1744")

	out, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-1744",
		"--status", "In Progress", "--assignee", "scott")
	if err != nil {
		t.Fatalf("issue observe returned error: %v", err)
	}
	// Reported against the issue now, not the project: an issue is the record
	// and a project merely references it.
	for _, want := range []string{"CDSS-1744", "status", "In Progress", "assignee", "synced_at"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ROZ1") {
		t.Errorf("output names a project; the observation is about the issue:\n%s", out)
	}
	_ = id
}

func TestProjectJiraFeed(t *testing.T) {
	db := initDB(t)
	jiraProject(t, db, "Split the nodepool", "CDSS-1744")
	jiraProject(t, db, "Retire the old one", "CDSS-1750")

	feed := `[
	  {"key": "CDSS-1744", "status": "In Progress", "sprint": "Sprint 42"},
	  {"key": "CDSS-1750", "status": "Done"},
	  {"key": "CDSS-9999", "status": "To Do"}
	]`
	out, err := runCLIWithInput(t, feed, "issue", "observe", "--db", db, "--feed", "-")
	if err != nil {
		t.Fatalf("issue observe --feed returned error: %v", err)
	}
	if !strings.Contains(out, "CDSS-9999 is tracked by no project") {
		t.Errorf("an issue nothing tracks was not reported:\n%s", out)
	}

	// The feed above says "sprint", which is the superseded name for
	// iteration; it still loads, and lands in the column that replaced it.
	if got := issueJSON(t, db, "CDSS-1744")["iteration"]; got != "Sprint 42" {
		t.Errorf("iteration = %#v, want Sprint 42", got)
	}
	if got := issueJSON(t, db, "CDSS-1750")["status"]; got != "Done" {
		t.Errorf("status = %#v, want Done", got)
	}
}

// TestJiraFeedAcceptsOneObject: one observation is the common case, and
// wrapping it in brackets is friction.
func TestJiraFeedAcceptsOneObject(t *testing.T) {
	db := initDB(t)
	jiraProject(t, db, "Split the nodepool", "CDSS-1744")

	_, err := runCLIWithInput(t, `{"key": "CDSS-1744", "status": "Done"}`,
		"issue", "observe", "--db", db, "--feed", "-")
	if err != nil {
		t.Fatalf("issue observe --feed returned error: %v", err)
	}
	if got := issueJSON(t, db, "CDSS-1744")["status"]; got != "Done" {
		t.Errorf("status = %#v, want Done", got)
	}
}

// TestJiraOmittedFieldIsNotACleared: the feed's fields are pointers so that
// omitted and empty stay distinguishable.
func TestJiraOmittedFieldIsNotACleared(t *testing.T) {
	db := initDB(t)
	jiraProject(t, db, "Split the nodepool", "CDSS-1744")

	if _, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-1744",
		"--assignee", "scott", "--iteration", "Sprint 42"); err != nil {
		t.Fatalf("issue observe returned error: %v", err)
	}
	if _, err := runCLIWithInput(t, `{"key": "CDSS-1744", "status": "Done"}`,
		"issue", "observe", "--db", db, "--feed", "-"); err != nil {
		t.Fatalf("issue observe --feed returned error: %v", err)
	}

	object := issueJSON(t, db, "CDSS-1744")
	if object["assignee"] != "scott" || object["iteration"] != "Sprint 42" {
		t.Errorf("issue = %#v, want the omitted fields left alone", object)
	}

	// An explicit empty one is a fact, and does clear.
	if _, err := runCLIWithInput(t, `{"key": "CDSS-1744", "assignee": ""}`,
		"issue", "observe", "--db", db, "--feed", "-"); err != nil {
		t.Fatalf("issue observe --feed returned error: %v", err)
	}
	if got := issueJSON(t, db, "CDSS-1744")["assignee"]; got != nil && got != "" {
		t.Errorf("assignee = %#v, want it cleared", got)
	}
}

// TestJiraHasNoActorFlag: the actor is fixed by the command, so there is no
// way to log this as sync:jira and claim the integration said it.
func TestJiraHasNoActorFlag(t *testing.T) {
	db := initDB(t)
	jiraProject(t, db, "Split the nodepool", "CDSS-1744")

	_, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-1744",
		"--status", "Done", "--actor", "sync:jira")
	if err == nil {
		t.Fatal("issue observe accepted --actor, want no such flag")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error = %v, want an unknown flag", err)
	}
}

func TestJiraRejections(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		args    []string
		wantErr string
	}{
		{
			name:    "no key and no feed",
			args:    []string{"issue", "observe"},
			wantErr: "name an issue key",
		},
		{
			name:    "nothing observed",
			args:    []string{"issue", "observe", "CDSS-1744"},
			wantErr: "nothing observed",
		},
		{
			name:    "a key as well as a feed",
			input:   `[{"key": "CDSS-1744", "status": "Done"}]`,
			args:    []string{"issue", "observe", "CDSS-1744", "--feed", "-"},
			wantErr: "reads the issue keys itself",
		},
		{
			name:    "a feed that is not JSON",
			input:   "CDSS-1744: Done",
			args:    []string{"issue", "observe", "--feed", "-"},
			wantErr: "not valid JSON",
		},
		{
			name:    "an entry with no key",
			input:   `[{"status": "Done"}]`,
			args:    []string{"issue", "observe", "--feed", "-"},
			wantErr: "has no key",
		},
		{
			name:    "an empty feed",
			input:   `[]`,
			args:    []string{"issue", "observe", "--feed", "-"},
			wantErr: "no observations",
		},
		{
			name:    "a vague timestamp",
			args:    []string{"issue", "observe", "CDSS-1744", "--status", "Done", "--at", "yesterday"},
			wantErr: "not a date or timestamp",
		},
		{
			name:    "a vague timestamp in the feed",
			input:   `[{"key": "CDSS-1744", "status": "Done", "synced_at": "yesterday"}]`,
			args:    []string{"issue", "observe", "--feed", "-"},
			wantErr: "not a date or timestamp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			jiraProject(t, db, "Split the nodepool", "CDSS-1744")

			_, err := runCLIWithInput(t, tt.input, append(tt.args, "--db", db)...)
			if err == nil {
				t.Fatalf("%v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestJiraSummaryCanBeSet: the column existed with no way to fill it, which
// made it dead surface. Both paths reach it — the flag and the feed.
func TestJiraSummaryCanBeSet(t *testing.T) {
	db := initDB(t)
	jiraProject(t, db, "Split the nodepool", "CDSS-1744")

	if _, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-1744",
		"--summary", "Move Walker nodepool definitions"); err != nil {
		t.Fatalf("issue observe --summary returned error: %v", err)
	}
	if got := issueJSON(t, db, "CDSS-1744")["summary"]; got != "Move Walker nodepool definitions" {
		t.Errorf("summary = %#v, want it set by the flag", got)
	}

	if _, err := runCLIWithInput(t, `{"key": "CDSS-1750", "summary": "from the feed"}`,
		"issue", "observe", "--db", db, "--feed", "-"); err != nil {
		t.Fatalf("issue observe --feed returned error: %v", err)
	}
	if got := issueJSON(t, db, "CDSS-1750")["summary"]; got != "from the feed" {
		t.Errorf("summary = %#v, want it set by the feed", got)
	}
}

// TestIssueListClosedAndSince is the end-of-week question, through the
// command: what closed, and what closed since Thursday.
func TestIssueListClosedAndSince(t *testing.T) {
	db := initDB(t)

	observe := func(key, status string, extra ...string) {
		t.Helper()
		args := append([]string{"issue", "observe", "--db", db, key,
			"--summary", key, "--status", status}, extra...)
		if _, err := runCLI(t, args...); err != nil {
			t.Fatalf("issue observe returned error: %v", err)
		}
	}
	observe("CDSS-1", "Done", "--closed-at", "2026-08-07")
	observe("CDSS-2", "Done", "--closed-at", "2026-08-05")
	observe("CDSS-3", "In Progress")

	closed, err := runCLI(t, "issue", "list", "--db", db, "--closed")
	if err != nil {
		t.Fatalf("issue list --closed returned error: %v", err)
	}
	if strings.Contains(closed, "CDSS-3") {
		t.Errorf("--closed listed an open issue:\n%s", closed)
	}
	// Oldest first, so the week reads in the order it happened.
	if before, after := strings.Index(closed, "CDSS-2"), strings.Index(closed, "CDSS-1"); before > after {
		t.Errorf("--closed is not in closing order:\n%s", closed)
	}
	if !strings.Contains(closed, "CLOSED") {
		t.Errorf("--closed does not show when:\n%s", closed)
	}

	window, err := runCLI(t, "issue", "list", "--db", db, "--since", "2026-08-06")
	if err != nil {
		t.Fatalf("issue list --since returned error: %v", err)
	}
	if !strings.Contains(window, "CDSS-1") || strings.Contains(window, "CDSS-2") {
		t.Errorf("--since did not cut the window at the date:\n%s", window)
	}
}

// TestIssueListRefusesAVagueDate: a date that is not one would compare as text
// and match nothing, which on a listing reads as an answer rather than as a
// mistake.
func TestIssueListRefusesAVagueDate(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "issue", "list", "--db", db, "--since", "last week"); err == nil {
		t.Fatal("issue list accepted a vague date")
	} else if !strings.Contains(err.Error(), "not a date or timestamp") {
		t.Errorf("error does not say what is wrong: %v", err)
	}
}

// TestObserveNeedsSomethingObserved keeps --closed-at inside the rule: it is
// an observation like any other, so it satisfies the check on its own.
func TestObserveNeedsSomethingObserved(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-1"); err == nil {
		t.Fatal("issue observe accepted an observation of nothing")
	} else if !strings.Contains(err.Error(), flagClosedAt) {
		t.Errorf("error does not offer --%s: %v", flagClosedAt, err)
	}

	if _, err := runCLI(t, "issue", "observe", "--db", db, "CDSS-1",
		"--closed-at", "2026-08-07"); err != nil {
		t.Fatalf("issue observe --closed-at alone returned error: %v", err)
	}
}
