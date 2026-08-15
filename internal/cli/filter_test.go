package cli

import (
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/github"
)

// observeStates records what GitHub says about several pull requests at once,
// which is the only way a state column gets filled: it is observed.
func observeStates(t *testing.T, db string, states map[string]string) {
	t.Helper()

	var prs []github.PullRequest
	for key, state := range states {
		repo, _, _ := strings.Cut(key, "#")
		prs = append(prs, github.PullRequest{
			Key: key, Repo: repo, State: state, Title: "A title for " + key,
		})
	}
	withFetcher(t, stubFetcher{result: github.Result{PullRequests: prs}})
	if _, err := runCLI(t, "sync", "github", "--db", db); err != nil {
		t.Fatalf("sync github returned error: %v", err)
	}
}

// TestFilterOnPRListRunsInSQL is the expression the experiment was asked for,
// end to end: it reaches SQLite as one query rather than being sifted in Go.
func TestFilterOnPRListRunsInSQL(t *testing.T) {
	db, _ := trackedPR(t)
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#2"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	observeStates(t, db, map[string]string{
		"owner/repo#1": "MERGED",
		"owner/repo#2": "OPEN",
	})

	out, err := runCLI(t, "pr", "list", "--db", db,
		"--filter", `state == "CLOSED" || state == "MERGED"`, "--fields", "id,state")
	if err != nil {
		t.Fatalf("pr list --filter returned error: %v", err)
	}
	if !strings.Contains(out, "owner/repo#1") {
		t.Errorf("the merged pull request is missing:\n%s", out)
	}
	if strings.Contains(out, "owner/repo#2") {
		t.Errorf("an open pull request survived the filter:\n%s", out)
	}
}

// TestFilterOnAListingWithNoPushdown: most listings hand their rows over
// already read, so the filter runs in Go. What matters is that it runs at all
// — the first version of this experiment reported "ran in SQL" for these and
// filtered nothing.
func TestFilterOnAListingWithNoPushdown(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "decide something", "--verb", "decide")
	addAction(t, db, "--title", "write something", "--verb", "write")

	out, err := runCLI(t, "action", "list", "--db", db,
		"--filter", `verb == "decide"`, "--fields", "id,verb,title")
	if err != nil {
		t.Fatalf("action list --filter returned error: %v", err)
	}
	if !strings.Contains(out, "decide something") {
		t.Errorf("the matching action is missing:\n%s", out)
	}
	if strings.Contains(out, "write something") {
		t.Errorf("a non-matching action survived the filter:\n%s", out)
	}
}

// TestExplainFilterSaysWhereItRan: whether a filter reached the query decides
// whether the listing read four rows or forty thousand, and nothing else on
// the screen would say which happened.
func TestExplainFilterSaysWhereItRan(t *testing.T) {
	db, _ := trackedPR(t)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "pushed down",
			args: []string{"pr", "list", "--filter", `state == "MERGED"`},
			want: "filter ran in SQL",
		},
		{
			name: "split",
			args: []string{"pr", "list", "--filter", `state == "MERGED" && repo.matches("^owner")`},
			want: "which ran in Go",
		},
		{
			name: "no pushdown on this listing",
			args: []string{"action", "list", "--filter", `verb == "decide"`},
			want: "does not push one down",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "--db", db, "--explain-filter")
			out, err := runCLI(t, args...)
			if err != nil {
				t.Fatalf("returned error: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output does not say %q:\n%s", tc.want, out)
			}
		})
	}
}

// TestAFilterNamingNothingIsRefused: the columns are the same vocabulary
// --fields and --sort accept, and CEL's own diagnostic is better than one of
// ours would be.
func TestAFilterNamingNothingIsRefused(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "action", "list", "--db", db, "--filter", `stat == "ready"`)
	if err == nil {
		t.Fatal("action list accepted a column that does not exist")
	}
	if !strings.Contains(err.Error(), "undeclared reference") {
		t.Errorf("error does not name the problem: %v", err)
	}
}
