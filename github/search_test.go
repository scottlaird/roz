package github

import (
	"context"
	"strings"
	"testing"
)

func TestSearchPullRequests(t *testing.T) {
	pages := []string{
		`{"data":{"search":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
			{"number":7,"repository":{"nameWithOwner":"acme/api"}},
			{}]}}}`,
		`{"data":{"search":{"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"nodes":[
			{"number":9,"repository":{"nameWithOwner":"acme/web"}}]}}}`,
	}
	var queries []string
	c := NewWithRunner(func(_ context.Context, q string) ([]byte, error) {
		queries = append(queries, q)
		return []byte(pages[len(queries)-1]), nil
	})
	keys, err := c.SearchPullRequests(context.Background(), "is:pr author:octocat", 10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, " ") != "acme/api#7 acme/web#9" {
		t.Errorf("keys = %v; an issue in the results should be skipped", keys)
	}
	if len(queries) != 2 || !strings.Contains(queries[1], `after: "c1"`) || !strings.Contains(queries[0], `"is:pr author:octocat"`) {
		t.Errorf("queries did not page: %v", queries)
	}
}

func TestSearchPullRequestsStopsAtLimit(t *testing.T) {
	calls := 0
	c := NewWithRunner(func(_ context.Context, q string) ([]byte, error) {
		calls++
		if !strings.Contains(q, "first: 1,") {
			t.Errorf("asked for more than the limit: %s", q)
		}
		return []byte(`{"data":{"search":{"pageInfo":{"hasNextPage":true,"endCursor":"c"},"nodes":[
			{"number":1,"repository":{"nameWithOwner":"acme/api"}}]}}}`), nil
	})
	keys, err := c.SearchPullRequests(context.Background(), "is:pr", 1)
	if err != nil || len(keys) != 1 || calls != 1 {
		t.Errorf("keys = %v, calls = %d, err = %v", keys, calls, err)
	}
}

func TestSearchPullRequestsReportsErrors(t *testing.T) {
	c := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return []byte(`{"errors":[{"message":"bad query"}]}`), nil
	})
	if _, err := c.SearchPullRequests(context.Background(), "is:pr", 5); err == nil || !strings.Contains(err.Error(), "bad query") {
		t.Errorf("error = %v", err)
	}
}
