package github

import (
	"context"
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
	query, aliases := buildRefQuery([]RefQuery{
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
	aliases := map[string]RefQuery{"ref0": {Repo: "acme/api", Prefix: "refs/tags/"}}
	if err := decodeRefsInto([]byte(body), aliases, &result); err != nil {
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
	query, _ := buildRefQuery([]RefQuery{
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
	aliases := map[string]RefQuery{"ref0": {Repo: "acme/api", Prefix: "refs/tags/"}}
	if err := decodeRefsInto([]byte(body), aliases, &result); err != nil {
		t.Fatalf("decodeRefsInto() returned error: %v", err)
	}
	if len(result.Refs) != 1 || result.Refs[0].Name != "service/s3/v1.107.0" {
		t.Errorf("decoded %+v, want the name kept whole", result.Refs)
	}
}
