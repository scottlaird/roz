package github

import (
	"context"
	"strings"
	"testing"
)

// realIssueResponse is the shape GitHub returns, taken from a live query. The
// third alias is a number that is a pull request rather than an issue, which
// GitHub nulls while answering the rest.
const realIssueResponse = `{
  "data": {
    "rateLimit": {"cost": 1, "remaining": 4997, "limit": 5000},
    "issue0": {"issue": {
      "number": 154, "title": "Choose the channel a review request is announced in",
      "state": "OPEN", "assignees": {"nodes": []}, "milestone": null
    }},
    "issue1": {"issue": {
      "number": 105, "title": "review_rule — table exists, no entity or command",
      "state": "CLOSED",
      "assignees": {"nodes": [{"login": "scottlaird"}, {"login": "someone"}]},
      "milestone": {"title": "v0.1.0"}
    }},
    "issue2": {"issue": null}
  },
  "errors": [{"message": "Could not resolve to an Issue with the number of 156."}]
}`

func TestIssuesReadsWhatResolved(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return []byte(realIssueResponse), nil
	})

	result, err := client.Issues(context.Background(), []string{
		"scottlaird/roz#154", "scottlaird/roz#105", "scottlaird/roz#156",
	})
	if err != nil {
		t.Fatalf("Issues() returned error: %v", err)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("read %d issues, want 2", len(result.Issues))
	}

	byKey := map[string]Issue{}
	for _, issue := range result.Issues {
		byKey[issue.Key] = issue
	}

	open := byKey["scottlaird/roz#154"]
	if open.State != "OPEN" || open.Title == "" {
		t.Errorf("open issue = %+v", open)
	}
	if open.Milestone != "" || len(open.Assignees) != 0 {
		t.Errorf("unassigned issue picked up %+v", open)
	}

	closed := byKey["scottlaird/roz#105"]
	if closed.State != "CLOSED" {
		t.Errorf("state = %q, want CLOSED", closed.State)
	}
	if closed.Milestone != "v0.1.0" {
		t.Errorf("milestone = %q", closed.Milestone)
	}
	if len(closed.Assignees) != 2 {
		t.Errorf("assignees = %v, want both", closed.Assignees)
	}
}

// TestAPullRequestNumberIsNotAnIssue: GitHub numbers them from one sequence,
// so the mistake is easy to make. It resolves to null rather than to something
// wrong, and saying so is the whole value.
func TestAPullRequestNumberIsNotAnIssue(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return []byte(realIssueResponse), nil
	})

	result, err := client.Issues(context.Background(), []string{
		"scottlaird/roz#154", "scottlaird/roz#105", "scottlaird/roz#156",
	})
	if err != nil {
		t.Fatalf("Issues() returned error: %v", err)
	}
	why, ok := result.Missing["scottlaird/roz#156"]
	if !ok {
		t.Fatalf("Missing = %v, want the pull request number", result.Missing)
	}
	if !strings.Contains(why, "issue") {
		t.Errorf("reason = %q, want it to say what is wrong", why)
	}
}

func TestIssuesAsksNothingForNoKeys(t *testing.T) {
	var called bool
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		called = true
		return []byte(`{"data":{}}`), nil
	})
	if _, err := client.Issues(context.Background(), nil); err != nil {
		t.Fatalf("Issues() returned error: %v", err)
	}
	if called {
		t.Error("Issues() sent a query with nothing to ask about")
	}
}

func TestBuildIssueQuery(t *testing.T) {
	query, aliases, err := buildIssueQuery([]string{"acme/api#7"})
	if err != nil {
		t.Fatalf("buildIssueQuery() returned error: %v", err)
	}
	if len(aliases) != 1 {
		t.Fatalf("built %d aliases, want 1", len(aliases))
	}
	for _, want := range []string{
		`repository(owner: "acme", name: "api")`, `issue(number: 7)`, "milestone", "assignees",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query does not contain %s:\n%s", want, query)
		}
	}
	// pullRequest would answer a different question, and answer it wrongly.
	if strings.Contains(query, "pullRequest") {
		t.Errorf("the issue query asks for a pull request:\n%s", query)
	}
}

func TestIssueKeyMustBeAKey(t *testing.T) {
	if _, _, err := buildIssueQuery([]string{"nonsense"}); err == nil {
		t.Error("buildIssueQuery accepted a key that names nothing")
	}
}
