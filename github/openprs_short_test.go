package github

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// openPRSearchResult is one page of the open pull request search: n pull requests
// numbered from first, with the count the search reported.
func openPRSearchResult(first, n, reported int, more bool, errors string) string {
	nodes := make([]string, n)
	for i := range nodes {
		nodes[i] = fmt.Sprintf(`{"number":%d,"files":{"pageInfo":{"hasNextPage":false},"nodes":[]}}`, first+i)
	}
	body := fmt.Sprintf(`{"data":{"search":{"issueCount":%d,"pageInfo":{"hasNextPage":%t,"endCursor":"c%d"},"nodes":[%s]}}`,
		reported, more, first+n, strings.Join(nodes, ","))
	if errors != "" {
		body += `,"errors":[{"message":"` + errors + `"}]`
	}
	return body + "}"
}

func TestOpenPullRequestsRereadsAShortListing(t *testing.T) {
	pages := []string{
		openPRSearchResult(1, 3, 40, false, ""), // ended early: no next page, 37 missing
		openPRSearchResult(1, 25, 40, true, ""),
		openPRSearchResult(26, 15, 40, false, ""),
	}
	calls := 0
	c := NewWithRunner(func(context.Context, string) ([]byte, error) {
		calls++
		return []byte(pages[calls-1]), nil
	})
	prs, err := c.OpenPullRequests(context.Background(), "acme/api", nil)
	if err != nil || len(prs) != 40 || calls != 3 {
		t.Fatalf("got %d pull requests in %d calls, err = %v; want the second listing's 40", len(prs), calls, err)
	}
}

func TestOpenPullRequestsFailsWhenAlwaysShort(t *testing.T) {
	calls := 0
	c := NewWithRunner(func(context.Context, string) ([]byte, error) {
		calls++
		return []byte(openPRSearchResult(1, 3, 40, false, "")), nil
	})
	_, err := c.OpenPullRequests(context.Background(), "acme/api", nil)
	if err == nil || !strings.Contains(err.Error(), "reported 40") || !strings.Contains(err.Error(), "returned 3") {
		t.Fatalf("err = %v, want one naming both counts", err)
	}
	if calls != openPRAttempts {
		t.Errorf("calls = %d, want %d attempts", calls, openPRAttempts)
	}
}

// The index lags, so a count a little off the listing is not a failure.
func TestOpenPullRequestsToleratesASmallGap(t *testing.T) {
	c := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return []byte(openPRSearchResult(1, 2, 3, false, "")), nil
	})
	if prs, err := c.OpenPullRequests(context.Background(), "acme/api", nil); err != nil || len(prs) != 2 {
		t.Fatalf("got %d, err = %v", len(prs), err)
	}
}

func TestOpenPullRequestsRejectsPartialData(t *testing.T) {
	c := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return []byte(openPRSearchResult(1, 2, 2, false, "Something went wrong while executing your query")), nil
	})
	if _, err := c.OpenPullRequests(context.Background(), "acme/api", nil); err == nil ||
		!strings.Contains(err.Error(), "Something went wrong") {
		t.Fatalf("err = %v, want the GraphQL error", err)
	}
}
