package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// realRefResponse is the shape GitHub returns for a refs query. The second
// alias is a repository the token cannot see, which GitHub nulls while
// answering the rest — the same partial-failure behaviour Fetch relies on.
const realRefResponse = `{
  "data": {
    "rateLimit": {"cost": 1, "remaining": 4998, "limit": 5000},
    "ref0": {"refs": {"nodes": [
      {"name": "v1.5.0", "target": {"oid": "aaa111"}},
      {"name": "v1.4.0", "target": {"oid": "bbb222"}}
    ]}},
    "ref1": null
  },
  "errors": [{"message": "Could not resolve to a Repository"}]
}`

func TestRefsReadsWhatResolvedAndReportsWhatDidNot(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return []byte(realRefResponse), nil
	})

	result, err := client.Refs(context.Background(), []RefQuery{
		{Repo: "acme/api", Prefix: "refs/tags/", Contains: "v"},
		{Repo: "acme/secret", Prefix: "refs/tags/", Contains: "v"},
	})
	if err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}

	if len(result.Refs) != 2 {
		t.Fatalf("Refs() read %d refs, want 2", len(result.Refs))
	}
	got := result.Refs[0]
	want := Ref{Repo: "acme/api", Prefix: "refs/tags/", Name: "v1.5.0", CommitSHA: "aaa111"}
	if got != want {
		t.Errorf("first ref = %+v, want %+v", got, want)
	}

	if why, ok := result.Missing["acme/secret"]; !ok {
		t.Error("a repository that would not resolve was not reported missing")
	} else if why == "" {
		t.Error("a missing repository was reported with no reason")
	}

	if result.RateLimit.Remaining != 4998 {
		t.Errorf("RateLimit.Remaining = %d, want 4998", result.RateLimit.Remaining)
	}
}

// TestRefsAsksNothingForNoQueries: a database with no outstanding wait must
// not spend a request finding that out.
func TestRefsAsksNothingForNoQueries(t *testing.T) {
	var called bool
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		called = true
		return []byte(`{"data":{}}`), nil
	})

	if _, err := client.Refs(context.Background(), nil); err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}
	if called {
		t.Error("Refs() sent a query with nothing to ask about")
	}
}

func TestBuildRefQuery(t *testing.T) {
	query, aliases := buildRefQueryFrom([]RefQuery{
		{Repo: "acme/api", Prefix: "refs/tags/", Contains: "v"},
		{Repo: "acme/web", Prefix: "refs/heads/", Contains: "release-"},
	})

	if len(aliases) != 2 {
		t.Fatalf("built %d aliases, want 2", len(aliases))
	}
	for _, want := range []string{
		`repository(owner: "acme", name: "api")`,
		`refPrefix: "refs/tags/"`,
		`query: "v"`,
		`refPrefix: "refs/heads/"`,
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query does not contain %s:\n%s", want, query)
		}
	}

	// Tags are ordered by commit date so the newest release is on the first
	// page; a branch has no such date, so it is ordered alphabetically.
	if !strings.Contains(query, "TAG_COMMIT_DATE") {
		t.Error("a tag query is not ordered by commit date")
	}
	if !strings.Contains(query, "ALPHABETICAL") {
		t.Error("a branch query is not ordered alphabetically")
	}
}

// TestDecodeRefsSkipsNodesWithNoTarget: a node with no oid is not a ref worth
// recording, and must not become one with an empty commit.
func TestDecodeRefsSkipsNodesWithNoTarget(t *testing.T) {
	body := `{"data": {"ref0": {"refs": {"nodes": [
	  {"name": "v1.5.0", "target": {"oid": "aaa"}},
	  {"name": "broken", "target": null},
	  {"name": "", "target": {"oid": "bbb"}}
	]}}}}`

	result := RefResult{Missing: map[string]string{}}
	aliases := map[string]aliasedQuery{"ref0": {query: RefQuery{Repo: "acme/api", Prefix: "refs/tags/"}}}
	if _, err := decodeRefsInto([]byte(body), aliases, &result); err != nil {
		t.Fatalf("decodeRefsInto() returned error: %v", err)
	}

	if len(result.Refs) != 1 || result.Refs[0].Name != "v1.5.0" {
		t.Errorf("decoded %+v, want only v1.5.0", result.Refs)
	}
}

// TestBuildRefQueryCarriesADirectoryPrefix: a monorepo tags api/v3.4.5, and
// the prefix has to reach GitHub as the filter — otherwise every tag in the
// repository comes back to be sorted out here.
func TestBuildRefQueryCarriesADirectoryPrefix(t *testing.T) {
	query, _ := buildRefQueryFrom([]RefQuery{
		{Repo: "acme/api", Prefix: "refs/tags/", Contains: "service/s3/v"},
	})
	if !strings.Contains(query, `query: "service/s3/v"`) {
		t.Errorf("query does not carry the directory prefix:\n%s", query)
	}
}

// TestDecodeRefsKeepsTheDirectoryPrefix: GitHub returns a ref under refPrefix
// without that prefix but *with* any directories below it, and the name has
// to be stored as given — it is what the pattern matches against.
func TestDecodeRefsKeepsTheDirectoryPrefix(t *testing.T) {
	body := `{"data": {"ref0": {"refs": {"nodes": [
	  {"name": "service/s3/v1.107.0", "target": {"oid": "aaa"}}
	]}}}}`

	result := RefResult{Missing: map[string]string{}}
	aliases := map[string]aliasedQuery{"ref0": {query: RefQuery{Repo: "acme/api", Prefix: "refs/tags/"}}}
	if _, err := decodeRefsInto([]byte(body), aliases, &result); err != nil {
		t.Fatalf("decodeRefsInto() returned error: %v", err)
	}
	if len(result.Refs) != 1 || result.Refs[0].Name != "service/s3/v1.107.0" {
		t.Errorf("decoded %+v, want the name kept whole", result.Refs)
	}
}

// buildRefQueryFrom is buildRefQuery for a first page of every query, which is
// what most of these tests are about.
func buildRefQueryFrom(queries []RefQuery) (string, map[string]aliasedQuery) {
	cursors := make(map[int]string, len(queries))
	for i := range queries {
		cursors[i] = ""
	}
	return buildRefQuery(queries, cursors)
}

// TestRefsPagesUntilExhausted: cli/cli has 200 tags and one page holds 100, so
// the second page has to be asked for or half the repository is invisible.
func TestRefsPagesUntilExhausted(t *testing.T) {
	var asked int
	client := NewWithRunner(func(_ context.Context, query string) ([]byte, error) {
		asked++
		if strings.Contains(query, "after:") {
			return []byte(refPageBody(2, 200, false, "")), nil
		}
		return []byte(refPageBody(1, 200, true, "CURSOR1")), nil
	})

	result, err := client.Refs(context.Background(), []RefQuery{
		{Repo: "cli/cli", Prefix: "refs/tags/", Contains: ""},
	})
	if err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}
	if asked != 2 {
		t.Errorf("asked %d times, want 2", asked)
	}
	if len(result.Refs) != 2 {
		t.Errorf("read %d refs, want both pages", len(result.Refs))
	}
	if len(result.Truncated) != 0 {
		t.Errorf("reported truncation for a filter it read to the end: %+v", result.Truncated)
	}
}

// TestRefsReportsAFilterItCannotReadThrough: aws/aws-sdk-go-v2 carries 82,000
// tags, and no number of pages is the right way to read those. What matters is
// that the shortfall is reported rather than absorbed — a wait against a filter
// like this may never see its ref.
func TestRefsReportsAFilterItCannotReadThrough(t *testing.T) {
	var asked int
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		asked++
		return []byte(refPageBody(asked, 82234, true, "CURSOR")), nil
	})

	result, err := client.Refs(context.Background(), []RefQuery{
		{Repo: "aws/aws-sdk-go-v2", Prefix: "refs/tags/", Contains: ""},
	})
	if err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}
	if asked != RefMaxPages {
		t.Errorf("asked %d times, want the bound of %d", asked, RefMaxPages)
	}
	if len(result.Truncated) != 1 {
		t.Fatalf("Truncated = %+v, want one entry", result.Truncated)
	}
	got := result.Truncated[0]
	if got.Matched != 82234 {
		t.Errorf("Matched = %d, want 82234", got.Matched)
	}
	if got.Read != RefMaxPages {
		t.Errorf("Read = %d, want %d", got.Read, RefMaxPages)
	}
	if got.Query.Repo != "aws/aws-sdk-go-v2" {
		t.Errorf("Query.Repo = %q", got.Query.Repo)
	}
}

// TestRefsStopsAskingAnExhaustedQuery: a narrow filter sharing a batch with a
// broad one must not be re-asked once it is done.
func TestRefsStopsAskingAnExhaustedQuery(t *testing.T) {
	var queries []string
	client := NewWithRunner(func(_ context.Context, query string) ([]byte, error) {
		queries = append(queries, query)
		// ref0 finishes at once; ref1 always has more.
		return []byte(`{"data": {
		  "ref0": {"refs": {"totalCount": 1, "pageInfo": {"hasNextPage": false, "endCursor": ""},
		    "nodes": [{"name": "v1.0.0", "target": {"oid": "a"}}]}},
		  "ref1": {"refs": {"totalCount": 9999, "pageInfo": {"hasNextPage": true, "endCursor": "C"},
		    "nodes": [{"name": "v2.0.0", "target": {"oid": "b"}}]}}
		}}`), nil
	})

	if _, err := client.Refs(context.Background(), []RefQuery{
		{Repo: "a/one", Prefix: "refs/tags/", Contains: "api/"},
		{Repo: "a/two", Prefix: "refs/tags/", Contains: ""},
	}); err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}

	if len(queries) < 2 {
		t.Fatalf("only asked %d times", len(queries))
	}
	for i, q := range queries[1:] {
		if strings.Contains(q, "a/one") {
			t.Errorf("page %d re-asked an exhausted query:\n%s", i+2, q)
		}
	}
}

// TestBuildRefQueryResumes: the second page has to carry a cursor, or paging
// re-reads the first one forever.
func TestBuildRefQueryResumes(t *testing.T) {
	query, _ := buildRefQuery(
		[]RefQuery{{Repo: "acme/api", Prefix: "refs/tags/", Contains: "v"}},
		map[int]string{0: "CURSOR9"})

	if !strings.Contains(query, `after: "CURSOR9"`) {
		t.Errorf("query does not resume from the cursor:\n%s", query)
	}
	// Counting is asked for once and read once. A connection big enough to
	// need paging is big enough that counting it five times is work for
	// nothing, and GitHub is already slow to serve those.
	if strings.Contains(query, "totalCount") {
		t.Errorf("a resumed page asks the connection to count itself again:\n%s", query)
	}

	// The first page must ask, though: without it a full page and a complete
	// answer are indistinguishable.
	first, _ := buildRefQueryFrom([]RefQuery{{Repo: "acme/api", Prefix: "refs/tags/"}})
	if !strings.Contains(first, "totalCount") {
		t.Errorf("the first page does not ask how many there are:\n%s", first)
	}
}

// TestRefsSurvivesAPageItCannotRead is what keeps one unreasonable repository
// from taking the whole sync down. GitHub times out serving deep pages of a
// connection with tens of thousands of refs, and that must degrade to "we read
// what we could" rather than to an error.
func TestRefsSurvivesAPageItCannotRead(t *testing.T) {
	var asked int
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		asked++
		if asked == 1 {
			return []byte(refPageBody(1, 82234, true, "CURSOR")), nil
		}
		// What a 504 actually looks like coming back through gh.
		return []byte(`{"message": "We couldn't respond to your request in time."}`), nil
	})

	result, err := client.Refs(context.Background(), []RefQuery{
		{Repo: "aws/aws-sdk-go-v2", Prefix: "refs/tags/"},
	})
	if err != nil {
		t.Fatalf("Refs() returned error for a page it could not read: %v", err)
	}
	if len(result.Refs) != 1 {
		t.Errorf("kept %d refs, want the page that did arrive", len(result.Refs))
	}
	if len(result.Truncated) != 1 {
		t.Fatalf("Truncated = %+v, want the shortfall reported", result.Truncated)
	}
	if got := result.Truncated[0].Matched; got != 82234 {
		t.Errorf("Matched = %d, want what the first page said", got)
	}
}

// TestRefsReportsARepositoryItCouldNotStart: a first page that fails leaves
// nothing to truncate, so it is reported as unreadable instead.
func TestRefsReportsARepositoryItCouldNotStart(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return []byte(`{"message": "boom"}`), nil
	})

	result, err := client.Refs(context.Background(), []RefQuery{
		{Repo: "acme/api", Prefix: "refs/tags/"},
	})
	if err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}
	if _, ok := result.Missing["acme/api"]; !ok {
		t.Errorf("Missing = %v, want the repository named", result.Missing)
	}
	if len(result.Truncated) != 0 {
		t.Errorf("Truncated = %+v, want nothing: no page was read", result.Truncated)
	}
}

// TestRefsStillPropagatesRateLimiting: waiting is the caller's business, and
// must not be mistaken for a repository that cannot be read.
func TestRefsStillPropagatesRateLimiting(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return nil, fmt.Errorf("%w: secondary rate limit", ErrRateLimited)
	})

	_, err := client.Refs(context.Background(), []RefQuery{{Repo: "acme/api", Prefix: "refs/tags/"}})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("Refs() returned %v, want it to report rate limiting", err)
	}
}

// refPageBody renders one page of a paginated response.
func refPageBody(n, total int, more bool, cursor string) string {
	return fmt.Sprintf(`{"data": {"ref0": {"refs": {
	  "totalCount": %d,
	  "pageInfo": {"hasNextPage": %t, "endCursor": %q},
	  "nodes": [{"name": "v1.%d.0", "target": {"oid": "sha%d"}}]
	}}}}`, total, more, cursor, n, n)
}

// TestRefsStopsWhenItRecognisesSomething is what makes a poll incremental. A
// repository with 800 tags and one new one should cost one request, not five:
// tags come back newest-first, so a page carrying something already recorded
// means the read has met what the last one left.
func TestRefsStopsWhenItRecognisesSomething(t *testing.T) {
	var asked int
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		asked++
		return []byte(`{"data": {"ref0": {"refs": {
		  "totalCount": 800,
		  "pageInfo": {"hasNextPage": true, "endCursor": "C"},
		  "nodes": [
		    {"name": "v2.1.0", "target": {"oid": "new"}},
		    {"name": "v2.0.0", "target": {"oid": "old"}}
		  ]}}}}`), nil
	})

	result, err := client.Refs(context.Background(), []RefQuery{{
		Repo: "acme/api", Prefix: "refs/tags/",
		Known: func(name string) bool { return name == "v2.0.0" },
	}})
	if err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}
	if asked != 1 {
		t.Errorf("asked %d times for a repository it had already read, want 1", asked)
	}
	if len(result.Refs) != 2 {
		t.Errorf("read %d refs, want the page it fetched", len(result.Refs))
	}
	// Catching up is finishing, not running short. Reporting it as truncation
	// would restate a non-problem on every poll for ever.
	if len(result.Truncated) != 0 {
		t.Errorf("Truncated = %+v, want nothing: the read caught up", result.Truncated)
	}
}

// TestRefsWalksWhenNothingIsKnown: the first read of a repository has no
// boundary to meet, so it uses the whole bound.
func TestRefsWalksWhenNothingIsKnown(t *testing.T) {
	var asked int
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		asked++
		return []byte(refPageBody(asked, 800, true, "C")), nil
	})

	result, err := client.Refs(context.Background(), []RefQuery{{
		Repo: "acme/api", Prefix: "refs/tags/",
		Known: func(string) bool { return false },
	}})
	if err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}
	if asked != RefMaxPages {
		t.Errorf("asked %d times on a first read, want the bound of %d", asked, RefMaxPages)
	}
	if len(result.Truncated) != 1 {
		t.Errorf("Truncated = %+v, want the shortfall reported", result.Truncated)
	}
}

// TestRefsWithNoKnownSetWalks: a nil Known is a first read by another name,
// and must not be mistaken for "everything is already recorded".
func TestRefsWithNoKnownSetWalks(t *testing.T) {
	var asked int
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		asked++
		return []byte(refPageBody(asked, 800, true, "C")), nil
	})

	if _, err := client.Refs(context.Background(),
		[]RefQuery{{Repo: "acme/api", Prefix: "refs/tags/"}}); err != nil {
		t.Fatalf("Refs() returned error: %v", err)
	}
	if asked != RefMaxPages {
		t.Errorf("asked %d times with no known set, want %d", asked, RefMaxPages)
	}
}

// TestSeriesIsAskedForAsARefPrefix is the difference between asking exactly
// and asking approximately. GitHub's `query` is a substring match, so
// "service/s3/" also matched 20 refs that are not under it; a ref prefix
// returns the 301 that are, and nothing else.
func TestSeriesIsAskedForAsARefPrefix(t *testing.T) {
	query, _ := buildRefQueryFrom([]RefQuery{
		{Repo: "aws/aws-sdk-go-v2", Prefix: "refs/tags/", Path: "service/s3"},
	})

	if !strings.Contains(query, `refPrefix: "refs/tags/service/s3/"`) {
		t.Errorf("the series is not asked for as a ref prefix:\n%s", query)
	}
	// And not as a substring, which would be the approximate question.
	if strings.Contains(query, `query: "service/s3/"`) {
		t.Errorf("the series is still being asked for as a substring:\n%s", query)
	}
}

// TestATopLevelQueryIsTheWholeNamespace: GitHub insists a ref prefix ends in a
// slash, so there is no way to ask for "tags starting with v". A top-level
// series is the whole namespace or nothing.
func TestATopLevelQueryIsTheWholeNamespace(t *testing.T) {
	query, _ := buildRefQueryFrom([]RefQuery{{Repo: "acme/api", Prefix: "refs/tags/"}})

	if !strings.Contains(query, `refPrefix: "refs/tags/"`) {
		t.Errorf("a top-level query is not the namespace:\n%s", query)
	}
}

// TestASeriesIsPutBackOnTheName: GitHub strips the whole ref prefix, series
// included, so `service/s3/v1.107.0` comes back as `v1.107.0`. A ref's name is
// what it is called, not what was left after the question — and the stored
// name is what every pattern matches against.
func TestASeriesIsPutBackOnTheName(t *testing.T) {
	body := `{"data": {"ref0": {"refs": {
	  "totalCount": 301,
	  "pageInfo": {"hasNextPage": false, "endCursor": ""},
	  "nodes": [{"name": "v1.107.0", "target": {"oid": "aaa"}}]
	}}}}`

	result := RefResult{Missing: map[string]string{}}
	aliases := map[string]aliasedQuery{"ref0": {query: RefQuery{
		Repo: "aws/aws-sdk-go-v2", Prefix: "refs/tags/", Path: "service/s3",
	}}}
	if _, err := decodeRefsInto([]byte(body), aliases, &result); err != nil {
		t.Fatalf("decodeRefsInto() returned error: %v", err)
	}
	if len(result.Refs) != 1 {
		t.Fatalf("decoded %d refs, want 1", len(result.Refs))
	}
	if got, want := result.Refs[0].Name, "service/s3/v1.107.0"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	// The namespace stays the namespace, so what kind of ref this is does not
	// depend on how it was asked for.
	if got := result.Refs[0].Prefix; got != "refs/tags/" {
		t.Errorf("prefix = %q, want the namespace", got)
	}
}

// TestKnownIsAskedAboutTheWholeName: the boundary that stops an incremental
// read is the set of names already stored, which carry their series.
func TestKnownIsAskedAboutTheWholeName(t *testing.T) {
	var asked []string
	body := `{"data": {"ref0": {"refs": {
	  "totalCount": 2,
	  "pageInfo": {"hasNextPage": false, "endCursor": ""},
	  "nodes": [{"name": "v1.107.0", "target": {"oid": "aaa"}}]
	}}}}`

	result := RefResult{Missing: map[string]string{}}
	aliases := map[string]aliasedQuery{"ref0": {query: RefQuery{
		Repo: "aws/aws-sdk-go-v2", Prefix: "refs/tags/", Path: "service/s3",
		Known: func(name string) bool {
			asked = append(asked, name)
			return false
		},
	}}}
	if _, err := decodeRefsInto([]byte(body), aliases, &result); err != nil {
		t.Fatalf("decodeRefsInto() returned error: %v", err)
	}
	if len(asked) != 1 || asked[0] != "service/s3/v1.107.0" {
		t.Errorf("Known was asked about %q, want the stored name", asked)
	}
}
