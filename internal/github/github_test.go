package github

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// realResponse is the shape GitHub actually returns, taken from a live query.
// Two aliases resolve; the third is a number that is not a pull request, so
// GitHub nulls it and adds an entry to errors while still answering the rest.
const realResponse = `{
  "data": {
    "rateLimit": {"cost": 1, "remaining": 4996},
    "pr0": {"pullRequest": {
      "number": 14108, "title": "a title", "url": "https://github.com/cli/cli/pull/14108",
      "state": "OPEN", "isDraft": false, "baseRefName": "trunk",
      "headRefOid": "abc123", "isInMergeQueue": false,
      "reviewDecision": "REVIEW_REQUIRED", "mergeStateStatus": "BLOCKED",
      "author": {"login": "someone"},
      "reviewRequests": {"nodes": [
        {"requestedReviewer": {"__typename": "Team", "slug": "platform"}},
        {"requestedReviewer": {"__typename": "User", "login": "reviewer1"}}
      ]},
      "latestOpinionatedReviews": {"nodes": [
        {"state": "APPROVED", "author": {"login": "approver1"}},
        {"state": "CHANGES_REQUESTED", "author": {"login": "grumpy"}}
      ]},
      "timelineItems": {"nodes": [{"createdAt": "2026-08-01T10:00:00Z"}]},
      "comments": {"nodes": [
        {"createdAt": "2026-08-02T11:00:00Z", "author": {"login": "someone", "__typename": "User"}},
        {"createdAt": "2026-08-02T12:00:00Z", "author": {"login": "reviewer1", "__typename": "User"}},
        {"createdAt": "2026-08-02T13:00:00Z", "author": {"login": "dependabot", "__typename": "Bot"}}
      ]},
      "reviews": {"nodes": []},
      "commits": {"nodes": [{"commit": {"statusCheckRollup": {
        "state": "FAILURE",
        "contexts": {"nodes": [
          {"__typename": "CheckRun", "name": "build", "conclusion": "SUCCESS"},
          {"__typename": "CheckRun", "name": "test", "conclusion": "FAILURE"},
          {"__typename": "StatusContext", "context": "ci/legacy", "state": "SUCCESS"}
        ]}
      }}}]}
    }},
    "pr1": {"pullRequest": {
      "number": 7, "title": "merged one", "url": "u", "state": "MERGED",
      "isDraft": false, "baseRefName": "main", "headRefOid": "def456",
      "isInMergeQueue": false, "reviewDecision": null, "mergeStateStatus": "UNKNOWN",
      "mergedAt": "2026-08-07T14:30:00Z",
      "author": {"login": "someone"},
      "reviewRequests": {"nodes": []},
      "latestOpinionatedReviews": {"nodes": []},
      "timelineItems": {"nodes": []},
      "comments": {"nodes": []},
      "reviews": {"nodes": []},
      "commits": {"nodes": [{"commit": {"statusCheckRollup": null}}]}
    }},
    "pr2": {"pullRequest": null}
  },
  "errors": [
    {"type": "NOT_FOUND", "path": ["pr2", "pullRequest"],
     "message": "Could not resolve to a PullRequest with the number of 999."}
  ]
}`

func fixedRunner(body string) Runner {
	return func(context.Context, string) ([]byte, error) { return []byte(body), nil }
}

func TestFetchDecodesARealResponse(t *testing.T) {
	client := NewWithRunner(fixedRunner(realResponse))

	result, err := client.Fetch(context.Background(),
		[]string{"cli/cli#14108", "owner/repo#7", "owner/repo#999"})
	if err != nil {
		t.Fatalf("Fetch() returned error: %v", err)
	}
	if len(result.PullRequests) != 2 {
		t.Fatalf("got %d pull requests, want 2", len(result.PullRequests))
	}

	byKey := map[string]PullRequest{}
	for _, pr := range result.PullRequests {
		byKey[pr.Key] = pr
	}

	open := byKey["cli/cli#14108"]
	if open.Repo != "cli/cli" || open.Number != 14108 {
		t.Errorf("key not split into repo and number: %+v", open)
	}
	if open.State != "OPEN" || open.ReviewDecision != "REVIEW_REQUIRED" || open.MergeStateStatus != "BLOCKED" {
		t.Errorf("state fields = %q/%q/%q", open.State, open.ReviewDecision, open.MergeStateStatus)
	}
	if open.ChecksState != "FAILURE" {
		t.Errorf("checks state = %q, want FAILURE", open.ChecksState)
	}
	if got, want := open.Checks["test"], "FAILURE"; got != want {
		t.Errorf("checks[test] = %q, want %q", got, want)
	}
	// A StatusContext folds in alongside CheckRuns.
	if got, want := open.Checks["ci/legacy"], "SUCCESS"; got != want {
		t.Errorf("checks[ci/legacy] = %q, want %q", got, want)
	}
	// The newest comment is a bot's and the oldest is the author's own; the
	// one in between is the only one that means somebody read this.
	if got := open.HumanCommentedAt; got != "2026-08-02T12:00:00Z" {
		t.Errorf("humanCommentedAt = %q, want the reviewer's comment", got)
	}
	if got := open.FirstReviewRequestedAt; got != "2026-08-01T10:00:00Z" {
		t.Errorf("firstReviewRequestedAt = %q", got)
	}
}

// TestReviewersKeepPeopleAndTeamsApart matters because one approval is not
// APPROVED when four teams are on the request.
func TestReviewersKeepPeopleAndTeamsApart(t *testing.T) {
	client := NewWithRunner(fixedRunner(realResponse))
	result, _ := client.Fetch(context.Background(), []string{"cli/cli#14108"})

	pr := result.PullRequests[0]
	want := []string{"@reviewer1", "platform"}
	if len(pr.ReviewerTeams) != 2 || pr.ReviewerTeams[0] != want[0] || pr.ReviewerTeams[1] != want[1] {
		t.Errorf("reviewers = %v, want %v", pr.ReviewerTeams, want)
	}
	// Only APPROVED counts; CHANGES_REQUESTED is not an approval.
	if len(pr.Approvals) != 1 || pr.Approvals[0] != "approver1" {
		t.Errorf("approvals = %v, want [approver1]", pr.Approvals)
	}
}

// TestUnknownMergeStateBecomesEmpty keeps a merged pull request's UNKNOWN
// from being recorded as an observation.
func TestUnknownMergeStateBecomesEmpty(t *testing.T) {
	client := NewWithRunner(fixedRunner(realResponse))
	result, _ := client.Fetch(context.Background(), []string{"cli/cli#14108", "owner/repo#7"})

	for _, pr := range result.PullRequests {
		if pr.Number == 7 && pr.MergeStateStatus != "" {
			t.Errorf("merged pull request kept mergeStateStatus %q, want it dropped", pr.MergeStateStatus)
		}
	}
}

// TestMergedAtIsRead: GitHub's own timestamp, which is what a week in review
// is ordered by. An open pull request has none, and its absence is what says
// "not merged" rather than anything roz decides.
func TestMergedAtIsRead(t *testing.T) {
	client := NewWithRunner(fixedRunner(realResponse))
	result, _ := client.Fetch(context.Background(), []string{"cli/cli#14108", "owner/repo#7"})

	for _, pr := range result.PullRequests {
		switch pr.Number {
		case 7:
			if pr.MergedAt != "2026-08-07T14:30:00Z" {
				t.Errorf("merged pull request has mergedAt %q", pr.MergedAt)
			}
		case 14108:
			if pr.MergedAt != "" {
				t.Errorf("open pull request has mergedAt %q, want none", pr.MergedAt)
			}
		}
	}
}

// TestPartialFailureKeepsTheRest is the behaviour the batching depends on.
func TestPartialFailureKeepsTheRest(t *testing.T) {
	client := NewWithRunner(fixedRunner(realResponse))

	result, err := client.Fetch(context.Background(),
		[]string{"cli/cli#14108", "owner/repo#7", "owner/repo#999"})
	if err != nil {
		t.Fatalf("Fetch() returned error: %v", err)
	}
	if len(result.Missing) != 1 {
		t.Fatalf("missing = %v, want one entry", result.Missing)
	}
	if _, ok := result.Missing["owner/repo#999"]; !ok {
		t.Errorf("missing = %v, want the unresolvable key", result.Missing)
	}
}

func TestFetchBatches(t *testing.T) {
	var calls int
	client := NewWithRunner(func(_ context.Context, query string) ([]byte, error) {
		calls++
		// Every alias in the batch must appear in the query.
		if !strings.Contains(query, "pr0:") {
			t.Errorf("query has no aliases:\n%s", query)
		}
		return []byte(`{"data":{}}`), nil
	})

	keys := make([]string, BatchSize*2+1)
	for i := range keys {
		keys[i] = "owner/repo#" + string(rune('0'+i%10))
	}
	if _, err := client.Fetch(context.Background(), keys); err != nil {
		t.Fatalf("Fetch() returned error: %v", err)
	}
	if want := 3; calls != want {
		t.Errorf("made %d requests for %d keys, want %d", calls, len(keys), want)
	}
}

func TestFetchReportsTransportFailure(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return nil, errors.New("gh: not authenticated")
	})

	if _, err := client.Fetch(context.Background(), []string{"owner/repo#1"}); err == nil {
		t.Error("Fetch() with a failing runner returned nil, want an error")
	}
}

func TestFetchRejectsAResponseWithNoData(t *testing.T) {
	client := NewWithRunner(fixedRunner(`{"errors":[{"message":"Bad credentials"}]}`))

	_, err := client.Fetch(context.Background(), []string{"owner/repo#1"})
	if err == nil {
		t.Fatal("Fetch() with no data returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("error = %v, want it to carry GitHub's message", err)
	}
}

func TestBuildQueryRejectsBadKeys(t *testing.T) {
	for _, key := range []string{"", "owner/repo", "repo#1", "owner/repo#x"} {
		if _, _, err := buildQuery([]string{key}); err == nil {
			t.Errorf("buildQuery(%q) returned nil, want an error", key)
		}
	}
}

func TestBuildQueryEscapesNames(t *testing.T) {
	query, aliases, err := buildQuery([]string{"owner/repo#1"})
	if err != nil {
		t.Fatalf("buildQuery() returned error: %v", err)
	}
	if !strings.Contains(query, `repository(owner: "owner", name: "repo")`) {
		t.Errorf("query does not quote the repository:\n%s", query)
	}
	if aliases["pr0"] != "owner/repo#1" {
		t.Errorf("aliases = %v, want pr0 mapped to the key", aliases)
	}
}
