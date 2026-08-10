package github

import (
	"fmt"
	"strconv"
	"strings"
)

// prFields is what one pull request contributes to a query.
//
// The vocabularies here are GitHub's, not ours — reviewDecision and
// mergeStateStatus in particular exist only in GraphQL, which is why this is
// not REST. mergeStateStatus needs no push access on the repository; it is
// UNKNOWN for merged and closed pull requests, and that is all.
//
// The nested connections are capped rather than paginated. A pull request
// with more than twenty requested reviewers or unresolved threads is beyond
// what this is for, and capping keeps one batch to one request.
const prFields = `
    number title url state isDraft baseRefName headRefOid isInMergeQueue
    reviewDecision mergeStateStatus
    author { login }
    reviewRequests(first: 20) {
      nodes { requestedReviewer { __typename ... on Team { slug } ... on User { login } } }
    }
    latestOpinionatedReviews(first: 20) {
      nodes { state author { login } } }
    timelineItems(first: 1, itemTypes: [REVIEW_REQUESTED_EVENT]) {
      nodes { ... on ReviewRequestedEvent { createdAt } }
    }
    comments(last: 1) { nodes { createdAt author { login } } }
    reviewThreads(first: 50) { nodes { isResolved isOutdated } }
    commits(last: 1) {
      nodes { commit { statusCheckRollup {
        state
        contexts(first: 50) {
          nodes {
            __typename
            ... on CheckRun { name conclusion }
            ... on StatusContext { context state }
          }
        }
      } } }
    }`

// buildQuery renders one aliased query, and the alias-to-key mapping needed
// to make sense of the response.
//
// Aliases are positional (pr0, pr1) rather than derived from the key, because
// a GraphQL alias must be a bare identifier and repository names contain
// characters that are not.
func buildQuery(keys []string) (query string, aliases map[string]string, err error) {
	if len(keys) == 0 {
		return "", nil, fmt.Errorf("no pull requests to fetch")
	}

	aliases = make(map[string]string, len(keys))
	var b strings.Builder
	b.WriteString("query {\n  rateLimit { cost remaining limit resetAt }\n")

	for i, key := range keys {
		owner, name, number, err := splitKey(key)
		if err != nil {
			return "", nil, err
		}
		alias := "pr" + strconv.Itoa(i)
		aliases[alias] = key

		fmt.Fprintf(&b, "  %s: repository(owner: %q, name: %q) { pullRequest(number: %d) {%s\n  } }\n",
			alias, owner, name, number, prFields)
	}
	b.WriteString("}\n")

	return b.String(), aliases, nil
}

// splitKey takes owner/repo#number apart. The store guarantees this shape,
// but the query is built from strings, so it is checked rather than assumed.
func splitKey(key string) (owner, name string, number int64, err error) {
	repo, digits, found := strings.Cut(key, "#")
	if !found {
		return "", "", 0, fmt.Errorf("%q is not a pull request key", key)
	}
	owner, name, found = strings.Cut(repo, "/")
	if !found || owner == "" || name == "" {
		return "", "", 0, fmt.Errorf("%q does not name owner/repo", key)
	}
	number, err = strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return "", "", 0, fmt.Errorf("%q has no pull request number", key)
	}
	return owner, name, number, nil
}
