package cli

import (
	"fmt"
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
			// Every listing pushes down now except `page list`, whose rows are
			// the slots rather than the table: a WHERE would drop the empty
			// ones, which is the opposite of what that listing is for.
			name: "the one listing that deliberately does not",
			args: []string{"page", "list", "--filter", `body != ""`},
			want: "does not push one down",
		},
		{
			name: "action list pushes down now",
			args: []string{"action", "list", "--filter", `verb == "decide"`},
			want: "filter ran in SQL",
		},
		{
			// The ranking joins project, and both tables have snooze_until, so
			// a bare column name would be ambiguous — the filter's terms are
			// qualified with the listing's own alias to stop that.
			name: "pushed down beside the ranking join",
			args: []string{"action", "list", "--sort", "priority", "--filter", `verb == "decide"`},
			want: "filter ran in SQL",
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

// TestNullAnswersTheSameWayWhicheverPathRan is the fix worth having end to
// end: a tracked pull request nothing has synced has a NULL state, and
// `state != "MERGED"` is true of it in CEL. SQLite would drop it, so the term
// is not pushed down — and the listing shows it either way.
func TestNullAnswersTheSameWayWhicheverPathRan(t *testing.T) {
	db, _ := trackedPR(t)
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#2"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	// One synced, one never — so one has a state and the other has NULL.
	observeStates(t, db, map[string]string{"owner/repo#1": "MERGED"})

	out, err := runCLI(t, "pr", "list", "--db", db,
		"--filter", `state != "MERGED"`, "--fields", "id,state", "--explain-filter")
	if err != nil {
		t.Fatalf("pr list --filter returned error: %v", err)
	}
	if !strings.Contains(out, "in Go") {
		t.Errorf("the term was pushed down, and SQL would drop the NULL row:\n%s", out)
	}
	if !strings.Contains(out, "owner/repo#2") {
		t.Errorf("the unsynced pull request was dropped; CEL says null != \"MERGED\":\n%s", out)
	}
	if strings.Contains(out, "owner/repo#1") {
		t.Errorf("the merged pull request survived a filter excluding it:\n%s", out)
	}
}

// TestARowWithNothingToCompareIsSkipped: a NULL number is not greater than
// five and not not-greater either. CEL calls that an error; failing the whole
// listing over one such row would be worse than any answer.
func TestARowWithNothingToCompareIsSkipped(t *testing.T) {
	db, _ := trackedPR(t)

	out, err := runCLI(t, "pr", "list", "--db", db,
		"--filter", `merged_at > "2026-01-01"`, "--fields", "id,merged_at")
	if err != nil {
		t.Fatalf("pr list --filter returned error: %v", err)
	}
	if strings.Contains(out, "owner/repo#1") {
		t.Errorf("a row with no value to compare was kept:\n%s", out)
	}
}

// TestEveryListingPushesItsFilterDown is #201 stated as a property rather than
// a list, so a listing added later is caught by the same test.
//
// page list is the exception and says why: its rows are the slots on the page
// rather than rows of a table, so a WHERE would drop the empty ones — and an
// empty slot is the thing that listing exists to show.
func TestEveryListingPushesItsFilterDown(t *testing.T) {
	db := initDB(t)

	tests := []struct {
		listing []string
		filter  string
	}{
		{[]string{"action", "list"}, `verb == "decide"`},
		{[]string{"project", "list"}, `status == "active"`},
		{[]string{"pr", "list"}, `state == "MERGED"`},
		{[]string{"issue", "list"}, `tracker == "github"`},
		{[]string{"ref", "list"}, `kind == "tag"`},
		{[]string{"repo", "list"}, `id == "owner/repo"`},
		{[]string{"verb", "list"}, `closes == "predicate"`},
		{[]string{"pipeline", "list"}, `active == true`},
		{[]string{"calendar", "list"}, `kind == "pto"`},
		{[]string{"owner", "list"}, `owner == "@example/backend"`},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.listing, " "), func(t *testing.T) {
			args := append(append([]string{}, tt.listing...),
				"--db", db, "--filter", tt.filter, "--explain-filter")
			out, err := runCLI(t, args...)
			if err != nil {
				t.Fatalf("%v returned error: %v", tt.listing, err)
			}
			if !strings.Contains(out, "filter ran in SQL") {
				t.Errorf("%v did not push its filter down:\n%s", tt.listing, out)
			}
		})
	}
}

// TestAChainSurvivesBeingSplit: a chain answered by the query, beside a term
// the query cannot take, still answers the same question.
//
// The Go half of a chain has its own tests in the filter package, where a
// loader can be counted. This is the end-to-end half: same database, same
// expression, and the split reported rather than assumed.
func TestAChainSurvivesBeingSplit(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/repo")

	// Two projects, one urgent, each with an action and a pull request, so a
	// chain has something to select and something to leave out.
	urgent := addProject(t, db, "urgent", "--priority", "1")
	later := addProject(t, db, "later", "--priority", "5")
	for i, project := range []string{urgent, later} {
		pr := fmt.Sprintf("owner/repo#%d", i+1)
		if _, err := runCLI(t, "pr", "track", "--db", db, pr); err != nil {
			t.Fatalf("pr track returned error: %v", err)
		}
		if _, err := runCLI(t, "action", "add", "--db", db, "--title", "do it",
			"--verb", "write", "--project", project, "--pr", pr); err != nil {
			t.Fatalf("action add returned error: %v", err)
		}
	}

	const expr = `actions.exists(a, a.project.priority == 1)`

	inSQL, err := runCLI(t, "pr", "list", "--db", db, "--filter", expr, "--fields", "id")
	if err != nil {
		t.Fatalf("pr list returned error: %v", err)
	}
	if !strings.Contains(inSQL, "owner/repo#1") || strings.Contains(inSQL, "owner/repo#2") {
		t.Fatalf("the query answered the chain wrongly:\n%s", inSQL)
	}

	// The same chain beside a term the query cannot take: the chain is still
	// answered by the query and the JSON term in Go, and the answer is the
	// one above. That split is what --explain-filter reports, and getting it
	// wrong in either direction shows up here as a different set of rows.
	both := expr + ` && approvals.exists(x, x == "nobody") == false`
	split, err := runCLI(t, "pr", "list", "--db", db, "--filter", both,
		"--fields", "id", "--explain-filter")
	if err != nil {
		t.Fatalf("pr list returned error: %v", err)
	}
	if !strings.Contains(split, "ran in Go") {
		t.Fatalf("the JSON term was meant to run in Go:\n%s", split)
	}
	if !strings.Contains(split, "owner/repo#1") || strings.Contains(split, "owner/repo#2") {
		t.Errorf("the split answered differently from the query alone:\n%s", split)
	}
}
