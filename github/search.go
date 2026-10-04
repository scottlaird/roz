package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// searchPage is GitHub's largest page for a search connection.
const searchPage = 100

// SearchPullRequests returns the keys of the pull requests a GitHub search
// finds, as owner/name#number, at most limit of them. query is GitHub's search
// syntax and should include is:pr, for example
// "is:pr is:open author:octocat org:acme". Issues the search also finds are
// skipped.
//
// Search is the one read that finds pull requests across repositories by
// who is involved, and what it returns is only keys: Fetch reads the rest,
// with the same fields a tracked pull request gets.
func (c *Client) SearchPullRequests(ctx context.Context, query string, limit int) ([]string, error) {
	var keys []string
	after := "null"
	for len(keys) < limit {
		first := min(searchPage, limit-len(keys))
		q := fmt.Sprintf(`query {
  rateLimit { cost remaining limit resetAt }
  search(query: %q, type: ISSUE, first: %d, after: %s) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest { number repository { nameWithOwner } } }
  }
}
`, query, first, after)
		body, err := c.request(ctx, ReadSearch, q)
		if errors.Is(err, ErrRateLimited) {
			return nil, err
		}
		var resp struct {
			Data *struct {
				Search struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Number     int64 `json:"number"`
						Repository *struct {
							NameWithOwner string `json:"nameWithOwner"`
						} `json:"repository"`
					} `json:"nodes"`
				} `json:"search"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if jerr := json.Unmarshal(body, &resp); jerr != nil || resp.Data == nil {
			if err != nil {
				return nil, fmt.Errorf("searching %q: %w", query, err)
			}
			if jerr != nil {
				return nil, fmt.Errorf("searching %q: decoding: %w", query, jerr)
			}
			return nil, fmt.Errorf("searching %q: %s", query, firstMessage(resp.Errors))
		}
		for _, n := range resp.Data.Search.Nodes {
			// An issue decodes as an empty node: no repository.
			if n.Repository != nil {
				keys = append(keys, fmt.Sprintf("%s#%d", n.Repository.NameWithOwner, n.Number))
			}
		}
		if !resp.Data.Search.PageInfo.HasNextPage {
			break
		}
		after = fmt.Sprintf("%q", resp.Data.Search.PageInfo.EndCursor)
	}
	return keys, nil
}
