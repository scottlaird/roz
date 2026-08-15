package github

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"context"
)

// issueFields is what one issue contributes to a query.
//
// The same shape the pull request query takes, and for the same reason: one
// request carries many issues, aliased positionally.
//
// assignees is capped rather than paginated. An issue with more than five
// people on it is not one this has anything useful to say about, and the
// column holds a name rather than a committee.
const issueFields = `
    number title state closedAt
    assignees(first: 5) { nodes { login } }
    milestone { title }`

// Issue is one tracker issue as GitHub reports it.
type Issue struct {
	// Key is owner/repo#number, which is how roz writes a GitHub issue and
	// what tracker_issue is keyed on.
	Key       string
	Title     string
	State     string
	Milestone string
	// ClosedAt is GitHub's own timestamp, empty while the issue is open. It
	// is what a week in review is ordered by; the poll that noticed the
	// closure is a different date.
	ClosedAt string
	// Assignees are logins, in the order GitHub gave them.
	Assignees []string
}

// IssueResult is what an issue read produced.
//
// Missing is not an error, for the reason it is not one when reading pull
// requests: an issue can be deleted, transferred, or invisible to this token,
// and the rest of the batch is still worth having.
type IssueResult struct {
	Issues    []Issue
	Missing   map[string]string
	RateLimit RateLimit
}

// Issues reads the given issues, in batches.
//
// A separate call from Fetch because it asks a different question of a
// different object, and because an issue is not a pull request even though
// GitHub numbers them from the same sequence — asking for one by the other's
// name returns nothing rather than something wrong, which is at least honest.
func (c *Client) Issues(ctx context.Context, keys []string) (IssueResult, error) {
	result := IssueResult{Missing: map[string]string{}}
	if len(keys) == 0 {
		return result, nil
	}

	for start := 0; start < len(keys); start += IssueBatchSize {
		keyBatch := keys[start:min(start+IssueBatchSize, len(keys))]
		b := batch{
			op: opIssues, entities: len(keyBatch),
			index: start/IssueBatchSize + 1, total: batches(len(keys), IssueBatchSize),
		}

		query, aliases, err := buildIssueQuery(keyBatch)
		if err != nil {
			return IssueResult{}, b.fail(err)
		}
		body, err := c.request(ctx, ReadIssues, query)
		if err != nil {
			return IssueResult{}, b.fail(err)
		}
		if err := decodeIssuesInto(body, aliases, &result); err != nil {
			return IssueResult{}, b.fail(err)
		}
	}
	return result, nil
}

// buildIssueQuery renders one aliased query and the alias-to-key mapping.
func buildIssueQuery(keys []string) (string, map[string]string, error) {
	aliases := make(map[string]string, len(keys))

	var b strings.Builder
	b.WriteString("query {\n  rateLimit { cost remaining limit resetAt }\n")
	for i, key := range keys {
		owner, name, number, err := splitKey(key)
		if err != nil {
			return "", nil, err
		}
		alias := "issue" + strconv.Itoa(i)
		aliases[alias] = key

		fmt.Fprintf(&b, "  %s: repository(owner: %q, name: %q) { issue(number: %d) {%s\n  } }\n",
			alias, owner, name, number, issueFields)
	}
	b.WriteString("}\n")

	return b.String(), aliases, nil
}

type wireIssue struct {
	Issue *struct {
		Number    int64  `json:"number"`
		Title     string `json:"title"`
		State     string `json:"state"`
		ClosedAt  string `json:"closedAt"`
		Assignees struct {
			Nodes []struct {
				Login string `json:"login"`
			} `json:"nodes"`
		} `json:"assignees"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
	} `json:"issue"`
}

func decodeIssuesInto(body []byte, aliases map[string]string, result *IssueResult) error {
	var response graphQLResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return unreadable(body, err)
	}
	if response.Data == nil {
		return noData(body, response.Errors)
	}

	if raw, ok := response.Data["rateLimit"]; ok {
		result.RateLimit = decodeRateLimit(raw)
	}

	for alias, key := range aliases {
		raw, ok := response.Data[alias]
		if !ok || string(raw) == "null" {
			result.Missing[key] = "GitHub returned nothing for it"
			continue
		}
		var w wireIssue
		if err := json.Unmarshal(raw, &w); err != nil {
			result.Missing[key] = fmt.Sprintf("could not read it: %v", err)
			continue
		}
		// A number that is a pull request rather than an issue resolves to
		// null here, which is the ordinary way to find out that a key names
		// the wrong kind of thing.
		if w.Issue == nil {
			result.Missing[key] = "is not an issue GitHub will show us"
			continue
		}

		issue := Issue{
			Key: key, Title: w.Issue.Title, State: w.Issue.State,
			ClosedAt: w.Issue.ClosedAt,
		}
		if w.Issue.Milestone != nil {
			issue.Milestone = w.Issue.Milestone.Title
		}
		for _, node := range w.Issue.Assignees.Nodes {
			issue.Assignees = append(issue.Assignees, node.Login)
		}
		result.Issues = append(result.Issues, issue)
	}
	return nil
}
