package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// RefPageSize is how many refs one query asks for per repository.
//
// GitHub caps a connection at 100. Tags come back newest-commit-first, so for
// the question this exists to answer — has the next release been cut — the
// interesting one is on the first page or the repository has had a hundred
// releases since anyone last synced.
const RefPageSize = 100

// RefQuery asks for the refs under one prefix in one repository.
//
// Prefix is a full ref namespace, refs/tags/ or refs/heads/. Contains narrows
// further and is an optimisation only: the caller matches its own pattern
// against whatever comes back, so a Contains that is too broad costs a larger
// response and nothing else.
type RefQuery struct {
	Repo     string // owner/name
	Prefix   string
	Contains string
}

// Ref is one branch or tag as GitHub reports it.
type Ref struct {
	Repo   string
	Prefix string
	// Name is the short form: GitHub returns refs under a prefix without it.
	Name      string
	CommitSHA string
}

// RefResult is what a ref read produced.
//
// Missing is keyed by repository rather than by ref, because the failure this
// reports is a repository that would not resolve. A ref that does not exist
// is not missing — it is the answer.
type RefResult struct {
	Refs      []Ref
	Missing   map[string]string
	RateLimit RateLimit
}

// Refs reads the refs matching each query.
//
// A separate call from Fetch rather than more fields on it: refs hang off the
// repository and pull requests off a number, so they do not share an alias,
// and a repository with no outstanding wait should not be asked at all.
func (c *Client) Refs(ctx context.Context, queries []RefQuery) (RefResult, error) {
	result := RefResult{Missing: map[string]string{}}
	if len(queries) == 0 {
		return result, nil
	}

	for start := 0; start < len(queries); start += BatchSize {
		batch := queries[start:min(start+BatchSize, len(queries))]

		query, aliases := buildRefQuery(batch)
		body, err := c.run(ctx, query)
		if err != nil {
			return RefResult{}, err
		}
		if err := decodeRefsInto(body, aliases, &result); err != nil {
			return RefResult{}, err
		}
	}
	return result, nil
}

// buildRefQuery renders one aliased query and the alias-to-query mapping.
//
// Ordered by the commit date descending for tags, which puts the newest
// release first, and alphabetically for anything else — TAG_COMMIT_DATE is
// meaningless on a branch. Both are GitHub's own orderings; nothing here
// depends on them beyond which page a ref lands on.
func buildRefQuery(queries []RefQuery) (string, map[string]RefQuery) {
	aliases := make(map[string]RefQuery, len(queries))

	var b strings.Builder
	b.WriteString("query {\n  rateLimit { cost remaining limit resetAt }\n")
	for i, q := range queries {
		alias := "ref" + strconv.Itoa(i)
		aliases[alias] = q

		owner, name, _ := strings.Cut(q.Repo, "/")
		order := "{field: ALPHABETICAL, direction: ASC}"
		if q.Prefix == "refs/tags/" {
			order = "{field: TAG_COMMIT_DATE, direction: DESC}"
		}
		fmt.Fprintf(&b,
			"  %s: repository(owner: %q, name: %q) { refs(refPrefix: %q, query: %q, first: %d, orderBy: %s) {\n"+
				"    nodes { name target { oid } }\n  } }\n",
			alias, owner, name, q.Prefix, q.Contains, RefPageSize, order)
	}
	b.WriteString("}\n")

	return b.String(), aliases
}

type wireRefs struct {
	Refs struct {
		Nodes []struct {
			Name   string `json:"name"`
			Target struct {
				OID string `json:"oid"`
			} `json:"target"`
		} `json:"nodes"`
	} `json:"refs"`
}

func decodeRefsInto(body []byte, aliases map[string]RefQuery, result *RefResult) error {
	var response graphQLResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("parsing the GraphQL response: %w", err)
	}
	if response.Data == nil {
		summary := summarise(response.Errors)
		if mentionsRateLimit(summary) {
			return fmt.Errorf("%w: %s", ErrRateLimited, summary)
		}
		return fmt.Errorf("GraphQL returned no data: %s", summary)
	}

	if raw, ok := response.Data["rateLimit"]; ok {
		result.RateLimit = decodeRateLimit(raw)
	}

	for alias, q := range aliases {
		raw, ok := response.Data[alias]
		if !ok || string(raw) == "null" {
			result.Missing[q.Repo] = "GitHub returned nothing for it"
			continue
		}
		var w wireRefs
		if err := json.Unmarshal(raw, &w); err != nil {
			result.Missing[q.Repo] = fmt.Sprintf("could not read its refs: %v", err)
			continue
		}
		for _, node := range w.Refs.Nodes {
			// A tag object rather than a commit still has an oid; an empty one
			// means GitHub gave us a node with no target, which is not a ref
			// worth recording.
			if node.Name == "" || node.Target.OID == "" {
				continue
			}
			result.Refs = append(result.Refs, Ref{
				Repo:      q.Repo,
				Prefix:    q.Prefix,
				Name:      node.Name,
				CommitSHA: node.Target.OID,
			})
		}
	}
	return nil
}
