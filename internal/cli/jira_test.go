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

	out, err := runCLI(t, "jira", "show", "--db", db, key, "-o", "json")
	if err != nil {
		t.Fatalf("jira show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return object
}

// projectJSON reads one project back as an object.
func projectJSON(t *testing.T, db, id string) map[string]any {
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

// jiraProject adds a project carrying a Jira key.
func jiraProject(t *testing.T, db, title, key string) string {
	t.Helper()
	return addProject(t, db, title, "--jira-key", key)
}

func TestProjectJira(t *testing.T) {
	db := initDB(t)
	id := jiraProject(t, db, "Split the nodepool", "CDSS-1744")

	out, err := runCLI(t, "project", "jira", "--db", db, "CDSS-1744",
		"--status", "In Progress", "--assignee", "scott")
	if err != nil {
		t.Fatalf("project jira returned error: %v", err)
	}
	// Reported against the issue now, not the project: an issue is the record
	// and a project merely references it.
	for _, want := range []string{"CDSS-1744", "status", "In Progress", "assignee", "synced_at"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TD1") {
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
	out, err := runCLIWithInput(t, feed, "project", "jira", "--db", db, "--feed", "-")
	if err != nil {
		t.Fatalf("project jira --feed returned error: %v", err)
	}
	if !strings.Contains(out, "CDSS-9999 is tracked by no project") {
		t.Errorf("an issue nothing tracks was not reported:\n%s", out)
	}

	if got := issueJSON(t, db, "CDSS-1744")["sprint"]; got != "Sprint 42" {
		t.Errorf("sprint = %#v, want Sprint 42", got)
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
		"project", "jira", "--db", db, "--feed", "-")
	if err != nil {
		t.Fatalf("project jira --feed returned error: %v", err)
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

	if _, err := runCLI(t, "project", "jira", "--db", db, "CDSS-1744",
		"--assignee", "scott", "--sprint", "Sprint 42"); err != nil {
		t.Fatalf("project jira returned error: %v", err)
	}
	if _, err := runCLIWithInput(t, `{"key": "CDSS-1744", "status": "Done"}`,
		"project", "jira", "--db", db, "--feed", "-"); err != nil {
		t.Fatalf("project jira --feed returned error: %v", err)
	}

	object := issueJSON(t, db, "CDSS-1744")
	if object["assignee"] != "scott" || object["sprint"] != "Sprint 42" {
		t.Errorf("issue = %#v, want the omitted fields left alone", object)
	}

	// An explicit empty one is a fact, and does clear.
	if _, err := runCLIWithInput(t, `{"key": "CDSS-1744", "assignee": ""}`,
		"project", "jira", "--db", db, "--feed", "-"); err != nil {
		t.Fatalf("project jira --feed returned error: %v", err)
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

	_, err := runCLI(t, "project", "jira", "--db", db, "CDSS-1744",
		"--status", "Done", "--actor", "sync:jira")
	if err == nil {
		t.Fatal("project jira accepted --actor, want no such flag")
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
			args:    []string{"project", "jira"},
			wantErr: "name an issue key",
		},
		{
			name:    "nothing observed",
			args:    []string{"project", "jira", "CDSS-1744"},
			wantErr: "nothing observed",
		},
		{
			name:    "a key as well as a feed",
			input:   `[{"key": "CDSS-1744", "status": "Done"}]`,
			args:    []string{"project", "jira", "CDSS-1744", "--feed", "-"},
			wantErr: "reads the issue keys itself",
		},
		{
			name:    "a feed that is not JSON",
			input:   "CDSS-1744: Done",
			args:    []string{"project", "jira", "--feed", "-"},
			wantErr: "not valid JSON",
		},
		{
			name:    "an entry with no key",
			input:   `[{"status": "Done"}]`,
			args:    []string{"project", "jira", "--feed", "-"},
			wantErr: "has no key",
		},
		{
			name:    "an empty feed",
			input:   `[]`,
			args:    []string{"project", "jira", "--feed", "-"},
			wantErr: "no observations",
		},
		{
			name:    "a vague timestamp",
			args:    []string{"project", "jira", "CDSS-1744", "--status", "Done", "--at", "yesterday"},
			wantErr: "not a date or timestamp",
		},
		{
			name:    "a vague timestamp in the feed",
			input:   `[{"key": "CDSS-1744", "status": "Done", "synced_at": "yesterday"}]`,
			args:    []string{"project", "jira", "--feed", "-"},
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

	if _, err := runCLI(t, "project", "jira", "--db", db, "CDSS-1744",
		"--summary", "Move Walker nodepool definitions"); err != nil {
		t.Fatalf("project jira --summary returned error: %v", err)
	}
	if got := issueJSON(t, db, "CDSS-1744")["summary"]; got != "Move Walker nodepool definitions" {
		t.Errorf("summary = %#v, want it set by the flag", got)
	}

	if _, err := runCLIWithInput(t, `{"key": "CDSS-1750", "summary": "from the feed"}`,
		"project", "jira", "--db", db, "--feed", "-"); err != nil {
		t.Fatalf("project jira --feed returned error: %v", err)
	}
	if got := issueJSON(t, db, "CDSS-1750")["summary"]; got != "from the feed" {
		t.Errorf("summary = %#v, want it set by the feed", got)
	}
}
