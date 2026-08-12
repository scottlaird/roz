package github

import (
	"context"
	"strings"
	"testing"
)

// scriptedRunner answers each call in turn, so a paginated fetch can be
// tested without pretending one response covers every round trip.
type scriptedRunner struct {
	replies []string
	queries []string
}

func (s *scriptedRunner) run(_ context.Context, query string) ([]byte, error) {
	s.queries = append(s.queries, query)
	if len(s.queries) > len(s.replies) {
		return nil, errUnexpectedCall
	}
	return []byte(s.replies[len(s.queries)-1]), nil
}

var errUnexpectedCall = errUnexpected{}

type errUnexpected struct{}

func (errUnexpected) Error() string { return "unexpected extra call" }

func filesReply(base string, more bool, cursor string, paths ...string) string {
	var nodes []string
	for _, p := range paths {
		nodes = append(nodes, `{"path":"`+p+`"}`)
	}
	return `{"data":{"rateLimit":{"cost":1,"remaining":4999,"limit":5000},
	  "repository":{"pullRequest":{
	    "baseRefName":"` + base + `",
	    "latestOpinionatedReviews":{"nodes":[
	      {"state":"APPROVED","author":{"login":"alice"}},
	      {"state":"CHANGES_REQUESTED","author":{"login":"grumpy"}}
	    ]},
	    "files":{"pageInfo":{"hasNextPage":` + boolText(more) + `,"endCursor":"` + cursor + `"},
	      "nodes":[` + strings.Join(nodes, ",") + `]}
	  }}}}`
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

const ownersReply = `{"data":{"repository":{
  "at0": null,
  "at1": {"text":"* @org/platform\n"},
  "at2": null
}}}`

func TestChangeFetchesFilesOwnersAndApprovals(t *testing.T) {
	runner := &scriptedRunner{replies: []string{
		filesReply("main", false, "", "README.md", "storage/a.go"),
		ownersReply,
	}}
	client := NewWithRunner(runner.run)

	change, err := client.Change(context.Background(), "owner/repo#1")
	if err != nil {
		t.Fatalf("Change() returned error: %v", err)
	}
	if got, want := change.BaseRef, "main"; got != want {
		t.Errorf("BaseRef = %q, want %q", got, want)
	}
	if got, want := len(change.Files), 2; got != want {
		t.Errorf("%d files, want %d", got, want)
	}
	if got, want := change.CodeownersPath, "CODEOWNERS"; got != want {
		t.Errorf("CodeownersPath = %q, want %q", got, want)
	}
	// Only approvals, not every review: a CHANGES_REQUESTED is not one.
	if got, want := strings.Join(change.Approvals, ","), "alice"; got != want {
		t.Errorf("Approvals = %q, want %q", got, want)
	}
}

// TestChangePaginatesTheFileList: an incomplete list could turn "no single
// owner covers this" into "one does", which is the wrong answer in the
// direction that matters.
func TestChangePaginatesTheFileList(t *testing.T) {
	runner := &scriptedRunner{replies: []string{
		filesReply("main", true, "CURSOR1", "a.go", "b.go"),
		filesReply("main", false, "", "c.go"),
		ownersReply,
	}}
	client := NewWithRunner(runner.run)

	change, err := client.Change(context.Background(), "owner/repo#1")
	if err != nil {
		t.Fatalf("Change() returned error: %v", err)
	}
	if got, want := len(change.Files), 3; got != want {
		t.Fatalf("%d files, want %d across two pages", got, want)
	}
	if !strings.Contains(runner.queries[1], `after: "CURSOR1"`) {
		t.Errorf("the second page did not use the cursor:\n%s", runner.queries[1])
	}
}

// TestChangeReadsCodeownersFromTheBaseRef: the rules that apply are the
// destination branch's, not the default branch's or the topic branch's.
func TestChangeReadsCodeownersFromTheBaseRef(t *testing.T) {
	runner := &scriptedRunner{replies: []string{
		filesReply("release-2.0", false, "", "a.go"),
		ownersReply,
	}}
	client := NewWithRunner(runner.run)

	if _, err := client.Change(context.Background(), "owner/repo#1"); err != nil {
		t.Fatalf("Change() returned error: %v", err)
	}
	if !strings.Contains(runner.queries[1], `"release-2.0:.github/CODEOWNERS"`) {
		t.Errorf("CODEOWNERS was not read from the base ref:\n%s", runner.queries[1])
	}
}

// TestChangeWithoutCodeowners: a repository with no such file requires no
// particular reviewer, which is an answer rather than a failure.
func TestChangeWithoutCodeowners(t *testing.T) {
	runner := &scriptedRunner{replies: []string{
		filesReply("main", false, "", "a.go"),
		`{"data":{"repository":{"at0":null,"at1":null,"at2":null}}}`,
	}}
	client := NewWithRunner(runner.run)

	change, err := client.Change(context.Background(), "owner/repo#1")
	if err != nil {
		t.Fatalf("Change() returned error: %v", err)
	}
	if change.Codeowners != "" || change.CodeownersPath != "" {
		t.Errorf("found CODEOWNERS %q at %q, want none", change.Codeowners, change.CodeownersPath)
	}
}

func TestChangeReportsAnUnreadablePullRequest(t *testing.T) {
	runner := &scriptedRunner{replies: []string{
		`{"data":{"repository":null},"errors":[{"message":"Could not resolve to a Repository"}]}`,
	}}
	client := NewWithRunner(runner.run)

	_, err := client.Change(context.Background(), "owner/repo#1")
	if err == nil {
		t.Fatal("Change() returned nil for an unreadable pull request")
	}
	if !strings.Contains(err.Error(), "Could not resolve") {
		t.Errorf("error = %v, want GitHub's reason", err)
	}
}
