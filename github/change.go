package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// codeownersPaths are the three places GitHub looks for a CODEOWNERS file, in
// the order it prefers them. The first that exists wins; the others are
// ignored even if present.
var codeownersPaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// filePage caps one round trip. GitHub will not return more than 100 nodes
// from a connection, so a larger number here is a request it would refuse.
const filePage = 100

// Change is what is needed to answer who has to approve a pull request: the
// paths it touches, the CODEOWNERS governing them, and who has approved so far.
type Change struct {
	Key string
	// BaseRef is the branch being merged into, and the ref CODEOWNERS is read
	// from — the rules that apply are the destination's, not the branch's.
	BaseRef string
	// Files is every path the pull request touches, across as many pages as
	// it took. Complete, or this returns an error: a missing file could turn
	// "one owner covers everything" from false into true, and a wrong answer
	// there is worse than no answer.
	Files []string
	// Codeowners is the file's contents, empty when the repository has none.
	Codeowners string
	// CodeownersPath says which of the three locations it came from, so a
	// person can go and look at the right one.
	CodeownersPath string
	// Approvals holds the logins that have approved. A review names a person,
	// and the files name teams, so resolving one to the other is the caller's
	// job — see the codeowners package.
	Approvals []string

	RateLimit RateLimit
}

// Change fetches everything needed to reason about one pull request's
// reviewers.
//
// Deliberately not part of the batched poll. The file list is large, changes
// only when someone pushes, and is wanted when a person asks a question rather
// than on every cycle — so paying for it per pull request per minute would be
// the wrong trade. This is the on-demand read.
func (c *Client) Change(ctx context.Context, key string) (Change, error) {
	owner, name, number, err := splitKey(key)
	if err != nil {
		return Change{}, err
	}
	change := Change{Key: key}

	// First page: the base ref, the approvals, and as many files as fit.
	cursor := ""
	for {
		page, err := c.filePage(ctx, owner, name, number, cursor)
		if err != nil {
			return Change{}, err
		}
		if change.BaseRef == "" {
			change.BaseRef = page.BaseRef
			change.Approvals = page.Approvals
		}
		change.Files = append(change.Files, page.Files...)
		change.RateLimit = page.RateLimit

		if !page.HasMore {
			break
		}
		cursor = page.Cursor
	}

	// CODEOWNERS from the base ref, once the base ref is known. A second round
	// trip rather than a guess at the default branch: a pull request into a
	// release branch is governed by that branch's rules.
	text, path, err := c.codeowners(ctx, owner, name, change.BaseRef)
	if err != nil {
		return Change{}, err
	}
	change.Codeowners, change.CodeownersPath = text, path
	return change, nil
}

// changePage is one round trip's worth of a pull request's files.
type changePage struct {
	BaseRef   string
	Approvals []string
	Files     []string
	HasMore   bool
	Cursor    string
	RateLimit RateLimit
}

func (c *Client) filePage(ctx context.Context, owner, name string, number int64, after string) (changePage, error) {
	cursor := "null"
	if after != "" {
		cursor = fmt.Sprintf("%q", after)
	}

	query := fmt.Sprintf(`query {
  rateLimit { cost remaining limit resetAt }
  repository(owner: %q, name: %q) {
    pullRequest(number: %d) {
      baseRefName
      latestOpinionatedReviews(first: 20) { nodes { state author { login } } }
      files(first: %d, after: %s) {
        pageInfo { hasNextPage endCursor }
        nodes { path }
      }
    }
  }
}
`, owner, name, number, filePage, cursor)

	body, err := c.request(ctx, ReadChange, query)
	if err != nil {
		return changePage{}, err
	}

	var decoded struct {
		Data struct {
			RateLimit  json.RawMessage `json:"rateLimit"`
			Repository *struct {
				PullRequest *struct {
					BaseRefName              string `json:"baseRefName"`
					LatestOpinionatedReviews struct {
						Nodes []struct {
							State  string `json:"state"`
							Author *struct {
								Login string `json:"login"`
							} `json:"author"`
						} `json:"nodes"`
					} `json:"latestOpinionatedReviews"`
					Files struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []struct {
							Path string `json:"path"`
						} `json:"nodes"`
					} `json:"files"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return changePage{}, fmt.Errorf("decoding the response: %w", err)
	}

	if decoded.Data.Repository == nil || decoded.Data.Repository.PullRequest == nil {
		return changePage{}, fmt.Errorf("%s/%s#%d could not be read: %s",
			owner, name, number, firstMessage(decoded.Errors))
	}
	pr := decoded.Data.Repository.PullRequest

	page := changePage{
		BaseRef:   pr.BaseRefName,
		HasMore:   pr.Files.PageInfo.HasNextPage,
		Cursor:    pr.Files.PageInfo.EndCursor,
		RateLimit: decodeRateLimit(decoded.Data.RateLimit),
	}
	for _, node := range pr.Files.Nodes {
		page.Files = append(page.Files, node.Path)
	}
	for _, review := range pr.LatestOpinionatedReviews.Nodes {
		if review.State == "APPROVED" && review.Author != nil {
			page.Approvals = append(page.Approvals, review.Author.Login)
		}
	}
	return page, nil
}

// codeowners reads the file from the given ref, trying each location GitHub
// does and taking the first that exists.
//
// One query with three aliases rather than three queries: a repository has at
// most one of them, and asking about all three costs the same as asking about
// the one that turns out to be there.
func (c *Client) codeowners(ctx context.Context, owner, name, ref string) (text, path string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("no base ref to read CODEOWNERS from")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "query {\n  repository(owner: %q, name: %q) {\n", owner, name)
	for i, candidate := range codeownersPaths {
		fmt.Fprintf(&b, "    at%d: object(expression: %q) { ... on Blob { text } }\n",
			i, ref+":"+candidate)
	}
	b.WriteString("  }\n}\n")

	body, err := c.request(ctx, ReadCodeowners, b.String())
	if err != nil {
		return "", "", err
	}

	var decoded struct {
		Data struct {
			Repository map[string]*struct {
				Text string `json:"text"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", "", fmt.Errorf("decoding CODEOWNERS: %w", err)
	}

	for i, candidate := range codeownersPaths {
		blob := decoded.Data.Repository[fmt.Sprintf("at%d", i)]
		if blob != nil && blob.Text != "" {
			return blob.Text, candidate, nil
		}
	}
	// No CODEOWNERS is a legitimate answer: the repository requires no
	// particular reviewer. Not an error.
	return "", "", nil
}

func firstMessage(errors []struct {
	Message string `json:"message"`
}) string {
	if len(errors) == 0 {
		return "no reason given"
	}
	return errors[0].Message
}
