package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// RefPageSize is how many refs one query asks for per repository. GitHub caps
// a ref connection at 100.
const RefPageSize = 100

// RefMaxPages bounds how far a single read will follow a repository's refs.
//
// It applies to the first read of a repository, which is the only one that
// walks history. After that a read stops as soon as it recognises a ref it
// already has, so a repository with eight hundred tags and one new one costs a
// single request — see RefQuery.Known.
//
// The bound exists because history can be arbitrarily long and some of it is
// not worth reading: aws/aws-sdk-go-v2 carries 82,000 tags, one per service
// release, and GitHub times out serving deep pages of a connection that size.
//
// Reaching it is reported, because it means history was not read to the end,
// and a wait for a ref that already exists might be looking for one of the
// ones that were never read. Refs created from then on are unaffected: they
// arrive at the top.
const RefMaxPages = 5

// RefQuery asks for the refs under one prefix in one repository.
//
// Prefix is a ref namespace, refs/tags/ or refs/heads/. Path narrows it to one
// series within that namespace — "service/s3" — and is a *prefix* match rather
// than a substring one, so it returns exactly what is under it. GitHub insists
// a ref prefix end in a slash, which is why a series can be asked for and a
// bare `v` cannot.
//
// Contains is the substring filter, and is for a name-shaped matcher whose
// literal head is worth narrowing on. It is an optimisation only: the caller
// matches its own pattern against whatever comes back, so a Contains that is
// too broad costs a larger response and nothing else.
type RefQuery struct {
	Repo   string // owner/name
	Prefix string
	// Path is the series, without a trailing slash. Empty is the whole
	// namespace, which for a monorepo is everything anybody has ever tagged.
	Path     string
	Contains string

	// Known reports whether a ref has already been recorded, and is what
	// makes a poll incremental. Tags come back newest-first, so a page
	// carrying one already seen means the read has caught up with what it had
	// and everything below is older still.
	//
	// Nil on a first read, when nothing is known and the whole bound is used.
	Known func(name string) bool
}

// Ref is one branch or tag as GitHub reports it.
type Ref struct {
	Repo string
	// Prefix is the namespace, refs/tags/ or refs/heads/, and never the
	// series: what kind of ref this is should not depend on how it was asked
	// for.
	Prefix string
	// Name is the short form, including its series: service/s3/v1.107.0.
	// GitHub returns a name with the whole ref prefix stripped, so the series
	// is put back here — a ref's name is what it is called, not what was left
	// after the question.
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

	// Truncated names the queries whose filter matched more refs than
	// RefMaxPages could read, with how many were matched and how many were
	// read. Not an error: what was read is still good, and every one of these
	// is a filter somebody should narrow.
	Truncated []RefTruncation
}

// RefTruncation reports a filter too broad to read to the end.
type RefTruncation struct {
	Query   RefQuery
	Matched int
	Read    int
}

// Refs reads the refs matching each query.
//
// A separate call from Fetch rather than more fields on it: refs hang off the
// repository and pull requests off a number, so they do not share an alias,
// and a repository with no outstanding wait should not be asked at all.
//
// Pages until each query is exhausted or RefMaxPages is reached, whichever
// comes first. Every round asks only about the queries that still have more,
// so a narrow filter costs one request even when it shares a batch with a
// broad one.
func (c *Client) Refs(ctx context.Context, queries []RefQuery) (RefResult, error) {
	result := RefResult{Missing: map[string]string{}}
	if len(queries) == 0 {
		return result, nil
	}

	for start := 0; start < len(queries); start += BatchSize {
		batch := queries[start:min(start+BatchSize, len(queries))]

		// cursors carries where each query has read up to; a query leaves the
		// map when it is exhausted or has failed.
		cursors := make(map[int]string, len(batch))
		for i := range batch {
			cursors[i] = ""
		}

		read := make(map[int]int, len(batch))
		var matched map[int]int

		// caughtUp holds the queries that stopped because they recognised
		// something, as distinct from the ones that ran out of pages.
		caughtUp := map[int]bool{}

		for page := 0; page < RefMaxPages && len(cursors) > 0; page++ {
			query, aliases := buildRefQuery(batch, cursors)

			body, err := c.run(ctx, query)
			if err == nil {
				var pageResult refPage
				pageResult, err = decodeRefsInto(body, aliases, &result)
				if err == nil {
					if matched == nil {
						matched = pageResult.matched
					}
					for i, n := range pageResult.read {
						read[i] += n
					}
					cursors = pageResult.next
					// A page carrying something already recorded means this
					// read has met what the last one left — but only where the
					// newest come first. Everything below is then older, so
					// there is nothing further worth asking for.
					//
					// Branches have no commit date to order by, so they come
					// back alphabetically and a new one sorts wherever its name
					// falls. Stopping on recognition there would mean a branch
					// named late in the alphabet was never seen at all: the
					// first page would be old and familiar for ever.
					for i := range pageResult.recognised {
						if !newestFirst(batch[i]) {
							continue
						}
						caughtUp[i] = true
						delete(cursors, i)
					}
					continue
				}
			}

			// Rate limiting is the caller's business: it means wait, not that
			// anything is wrong with the question.
			if errors.Is(err, ErrRateLimited) {
				return RefResult{}, err
			}

			// Anything else stops this batch where it is rather than failing
			// the sync. A connection large enough to need paging is also large
			// enough for GitHub to time out serving it — aws/aws-sdk-go-v2's
			// 82,000 tags answer page four about two times in three — and a
			// repository nobody can read to the end must not take the pull
			// request poll down with it.
			//
			// A first page that fails leaves nothing to report but the
			// failure, so those queries are reported as unreadable. Later ones
			// keep what they have and are reported as truncated below, which
			// is what they are.
			for i := range cursors {
				if read[i] == 0 {
					result.Missing[batch[i].Repo] = fmt.Sprintf("could not read its refs: %v", err)
					delete(cursors, i)
				}
			}
			break
		}

		// Anything still holding a cursor had more to give than the bound
		// allowed. A query that caught up is not among them: it stopped
		// because it was finished, not because it ran out of room.
		for i := range cursors {
			if caughtUp[i] {
				continue
			}
			result.Truncated = append(result.Truncated, RefTruncation{
				Query: batch[i], Matched: matched[i], Read: read[i],
			})
		}
	}
	return result, nil
}

// refPage is what one round of paging learned.
type refPage struct {
	// next holds the queries with more to read, by index, and where to resume.
	next map[int]string
	// read is how many refs each query contributed this round.
	read map[int]int
	// matched is what GitHub says the filter matches in total.
	matched map[int]int
	// recognised holds the queries whose page carried a ref already recorded.
	recognised map[int]bool
}

// aliasedQuery ties a GraphQL alias back to what asked for it.
type aliasedQuery struct {
	index int
	query RefQuery
}

// buildRefQuery renders one aliased query for the given cursors, and the
// alias-to-query mapping needed to read the response.
//
// Ordered by the commit date descending for tags, which puts the newest
// release first, and alphabetically for anything else — TAG_COMMIT_DATE is
// meaningless on a branch. Both are GitHub's own orderings; nothing here
// depends on them beyond which page a ref lands on.
//
// totalCount comes back on every page and is what makes truncation detectable:
// without it, a full page and a filter with exactly one page of matches look
// identical.
func buildRefQuery(queries []RefQuery, cursors map[int]string) (string, map[string]aliasedQuery) {
	aliases := make(map[string]aliasedQuery, len(cursors))

	// Sorted, so a query is deterministic and testable.
	indexes := make([]int, 0, len(cursors))
	for i := range cursors {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)

	var b strings.Builder
	b.WriteString("query {\n  rateLimit { cost remaining limit resetAt }\n")
	for _, i := range indexes {
		q := queries[i]
		alias := "ref" + strconv.Itoa(i)
		aliases[alias] = aliasedQuery{index: i, query: q}

		owner, name, _ := strings.Cut(q.Repo, "/")
		order := "{field: ALPHABETICAL, direction: ASC}"
		if newestFirst(q) {
			order = "{field: TAG_COMMIT_DATE, direction: DESC}"
		}
		refPrefix := q.Prefix + seriesPath(q.Path)
		// totalCount only on the first page. It is read once, and asking a
		// large connection to count itself on every page is work for nothing.
		after, count := "", "    totalCount\n"
		if cursors[i] != "" {
			after, count = fmt.Sprintf(", after: %q", cursors[i]), ""
		}
		fmt.Fprintf(&b,
			"  %s: repository(owner: %q, name: %q) { refs(refPrefix: %q, query: %q, first: %d, orderBy: %s%s) {\n"+
				"%s    pageInfo { hasNextPage endCursor }\n"+
				"    nodes { name target { oid } }\n  } }\n",
			alias, owner, name, refPrefix, q.Contains, RefPageSize, order, after, count)
	}
	b.WriteString("}\n")

	return b.String(), aliases
}

type wireRefs struct {
	Refs struct {
		TotalCount int `json:"totalCount"`
		PageInfo   struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []struct {
			Name   string `json:"name"`
			Target struct {
				OID string `json:"oid"`
			} `json:"target"`
		} `json:"nodes"`
	} `json:"refs"`
}

func decodeRefsInto(body []byte, aliases map[string]aliasedQuery, result *RefResult) (refPage, error) {
	page := refPage{
		next:       map[int]string{},
		read:       map[int]int{},
		matched:    map[int]int{},
		recognised: map[int]bool{},
	}

	var response graphQLResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return page, fmt.Errorf("parsing the GraphQL response: %w", err)
	}
	if response.Data == nil {
		summary := summarise(response.Errors)
		if mentionsRateLimit(summary) {
			return page, fmt.Errorf("%w: %s", ErrRateLimited, summary)
		}
		return page, fmt.Errorf("GraphQL returned no data: %s", summary)
	}

	if raw, ok := response.Data["rateLimit"]; ok {
		result.RateLimit = decodeRateLimit(raw)
	}

	for alias, aq := range aliases {
		q := aq.query
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

		if w.Refs.TotalCount > 0 {
			page.matched[aq.index] = w.Refs.TotalCount
		}
		page.read[aq.index] = len(w.Refs.Nodes)
		if w.Refs.PageInfo.HasNextPage && w.Refs.PageInfo.EndCursor != "" {
			page.next[aq.index] = w.Refs.PageInfo.EndCursor
		}

		for _, node := range w.Refs.Nodes {
			// A tag object rather than a commit still has an oid; an empty one
			// means GitHub gave us a node with no target, which is not a ref
			// worth recording.
			if node.Name == "" || node.Target.OID == "" {
				continue
			}
			if q.Known != nil && q.Known(seriesPath(q.Path)+node.Name) {
				page.recognised[aq.index] = true
			}
			result.Refs = append(result.Refs, Ref{
				Repo:   q.Repo,
				Prefix: q.Prefix,
				// GitHub strips the whole ref prefix, series included, so the
				// series goes back on: the name is what the ref is called.
				Name:      seriesPath(q.Path) + node.Name,
				CommitSHA: node.Target.OID,
			})
		}
	}
	return page, nil
}

// newestFirst reports whether this query comes back with the most recent ref
// at the top.
//
// Only tags can: GitHub's RefOrderField offers ALPHABETICAL and
// TAG_COMMIT_DATE, and a branch has no commit date. It decides both the
// ordering asked for and whether a read may stop as soon as it recognises
// something, because the second only follows from the first.
func newestFirst(q RefQuery) bool { return q.Prefix == "refs/tags/" }

// seriesPath renders a series for use as part of a ref prefix, which GitHub
// insists ends in a slash. Empty stays empty: the namespace is already a
// prefix in its own right.
func seriesPath(path string) string {
	if path == "" {
		return ""
	}
	return path + "/"
}
