package github

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
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

	keys := make([]string, PRBatchSize*2+1)
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

func TestFetchFailureNamesTheRequest(t *testing.T) {
	// Fail the second batch, so the reported position is not the one a
	// hardcoded "1 of 1" would also produce.
	var calls int
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("connection reset")
		}
		return []byte(`{"data":{}}`), nil
	})

	keys := make([]string, PRBatchSize+3)
	for i := range keys {
		keys[i] = "owner/repo#1"
	}

	_, err := client.Fetch(context.Background(), keys)
	if err == nil {
		t.Fatal("Fetch() with a failing runner returned nil, want an error")
	}
	for _, want := range []string{"pull requests", "batch 2 of 2", "3 entities", "connection reset"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestIssuesFailureSaysItWasIssues(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return nil, errors.New("connection reset")
	})

	_, err := client.Issues(context.Background(), []string{"owner/repo#1", "owner/repo#2"})
	if err == nil {
		t.Fatal("Issues() with a failing runner returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "issues") {
		t.Errorf("error = %q, want it to name the operation", err)
	}
	if strings.Contains(err.Error(), "pull requests") {
		t.Errorf("error = %q, want it not to claim it was reading pull requests", err)
	}
	if !strings.Contains(err.Error(), "2 entities") {
		t.Errorf("error = %q, want it to say how many it asked about", err)
	}
}

func TestRequestIdentityKeepsARateLimitRecognisable(t *testing.T) {
	client := NewWithRunner(fixedRunner(`{"errors":[{"message":"API rate limit exceeded"}]}`))

	_, err := client.Fetch(context.Background(), []string{"owner/repo#1"})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("error = %v, want it to still be an ErrRateLimited", err)
	}
}

func TestNonJSONBodyKeepsWhatGhSaid(t *testing.T) {
	err := ghFailure(errors.New("exit status 1"), "gh: Bad gateway (HTTP 502)", []byte("<html><body>502</body></html>"))
	if err == nil {
		t.Fatal("ghFailure() returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "Bad gateway") {
		t.Errorf("error = %q, want it to carry stderr", err)
	}
	if !strings.Contains(err.Error(), "<html>") {
		t.Errorf("error = %q, want it to quote the body", err)
	}
}

func TestAnHTMLLimitPageIsARateLimit(t *testing.T) {
	// The limit is announced in the page rather than on stderr, which is the
	// case that used to take the escalating backoff instead of waiting.
	err := ghFailure(nil, "", []byte("<html><body>You have exceeded a secondary rate limit</body></html>"))
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("error = %v, want an ErrRateLimited", err)
	}
}

func TestLooksJSONSeparatesBodiesFromPages(t *testing.T) {
	for _, body := range []string{`{"data":{}}`, "\n  {\"data\":{}}"} {
		if !looksJSON([]byte(body)) {
			t.Errorf("looksJSON(%q) = false, want true", body)
		}
	}
	for _, body := range []string{"<html>", "", "not json"} {
		if looksJSON([]byte(body)) {
			t.Errorf("looksJSON(%q) = true, want false", body)
		}
	}
}

func TestExcerptIsBounded(t *testing.T) {
	body := []byte(strings.Repeat("x", 40000))

	got := excerpt(body)
	if len(got) > excerptMax+64 {
		t.Errorf("excerpt is %d long, want it bounded near %d", len(got), excerptMax)
	}
	if !strings.Contains(got, "of 40000 bytes") {
		t.Errorf("excerpt = %q, want it to report the full size", got)
	}
}

func TestExcerptFlattensAndKeepsShortBodiesWhole(t *testing.T) {
	got := excerpt([]byte("<html>\n  <body>502</body>\n</html>"))
	if strings.Contains(got, "\n") {
		t.Errorf("excerpt = %q, want it on one line", got)
	}
	if strings.Contains(got, "of ") {
		t.Errorf("excerpt = %q, want no truncation note for a short body", got)
	}
}

func TestExcerptCutsOnARuneBoundary(t *testing.T) {
	got := excerpt([]byte(strings.Repeat("é", excerptMax)))
	if !utf8.ValidString(got) {
		t.Errorf("excerpt = %q, want valid UTF-8", got)
	}
}

func TestNoDataAndNoErrorQuotesTheBody(t *testing.T) {
	client := NewWithRunner(fixedRunner(`{"data":null,"errors":[]}`))

	_, err := client.Fetch(context.Background(), []string{"owner/repo#1"})
	if err == nil {
		t.Fatal("Fetch() with no data returned nil, want an error")
	}
	if !strings.Contains(err.Error(), `errors\":[]`) {
		t.Errorf("error = %q, want it to quote the body it could not use", err)
	}
}

func TestUnreadableBodyQuotesIt(t *testing.T) {
	client := NewWithRunner(fixedRunner(`{"data": truncated`))

	_, err := client.Fetch(context.Background(), []string{"owner/repo#1"})
	if err == nil {
		t.Fatal("Fetch() with an unparseable body returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error = %q, want it to quote the body", err)
	}
}

// TestBatchSizesAreSeparate is the point of splitting one constant into
// three: retuning the read that fails must not retune the two that do not.
//
// A ref query is a repository and a namespace that pages internally, so its
// per-entity cost is not comparable to a pull request's — which carries
// reviews, threads, checks and a timeline in the same request.
func TestBatchSizesAreSeparate(t *testing.T) {
	if PRBatchSize >= IssueBatchSize {
		t.Errorf("PRBatchSize (%d) is not below IssueBatchSize (%d); the pull request "+
			"query is the heavy one", PRBatchSize, IssueBatchSize)
	}
	// The failing case was 42 entities in one request, so the ceiling sits
	// below that rather than at the 150 the old constant was chosen against.
	if PRBatchSize >= 42 {
		t.Errorf("PRBatchSize = %d, which is at or above the size observed to fail", PRBatchSize)
	}
}

func TestBatches(t *testing.T) {
	for _, tt := range []struct {
		n, size, want int
	}{
		{0, 20, 0},
		{1, 20, 1},
		{20, 20, 1},
		{21, 20, 2},
		{42, 20, 3},
		{42, 50, 1},
	} {
		if got := batches(tt.n, tt.size); got != tt.want {
			t.Errorf("batches(%d, %d) = %d, want %d", tt.n, tt.size, got, tt.want)
		}
	}
}
