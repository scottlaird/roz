package filter

import (
	"database/sql"
	"strings"
	"testing"
)

// row is a record shaped like roz's, without being one: a text column, a
// nullable one, an integer, and a JSON list.
type row struct {
	ID        string         `db:"id" kind:"identity"`
	State     sql.NullString `db:"state" kind:"observed"`
	Title     string         `db:"title"`
	Number    int64          `db:"number"`
	MergedAt  sql.NullString `db:"merged_at" kind:"observed"`
	Approvals string         `db:"approvals" kind:"observed" format:"json"`
}

func compile(t *testing.T, expr string) *Filter {
	t.Helper()
	f, err := Compile(&row{}, expr)
	if err != nil {
		t.Fatalf("Compile(%q) returned error: %v", expr, err)
	}
	return f
}

// TestTheExampleIsOneQuery is the expression the experiment was asked for. It
// has to reach SQLite whole: an or is the case where filtering in Go after the
// fact is most obviously the wrong shape.
func TestTheExampleIsOneQuery(t *testing.T) {
	f := compile(t, `state == "CLOSED" || state == "MERGED"`)

	where, args := f.SQL()
	if want := "(state = ? OR state = ?)"; where != want {
		t.Errorf("SQL = %q, want %q", where, want)
	}
	if len(args) != 2 || args[0] != "CLOSED" || args[1] != "MERGED" {
		t.Errorf("args = %v, want the two states", args)
	}
	if f.residual != nil {
		t.Errorf("something was left for Go: %s", f.residualSrc)
	}
}

// TestValuesAreParameters: the values in a filter are somebody's typing, and a
// WHERE clause concatenated from typing is the oldest mistake there is.
func TestValuesAreParameters(t *testing.T) {
	f := compile(t, `title.contains("' OR 1=1 --")`)

	where, args := f.SQL()
	if strings.Contains(where, "OR 1=1") {
		t.Errorf("a value was inlined into the SQL: %q", where)
	}
	if len(args) != 1 || args[0] != "' OR 1=1 --" {
		t.Errorf("args = %v, want the string as one parameter", args)
	}
}

// TestOneAwkwardTermDoesNotCostTheRest is the third option in the brief: split
// the expression, push down what converts, run the rest in Go.
func TestOneAwkwardTermDoesNotCostTheRest(t *testing.T) {
	// matches() is not supported by cel2sql's SQLite dialect, which makes it
	// the natural example of a term that has to run in Go.
	f := compile(t, `state == "MERGED" && title.matches("^A ")`)

	where, args := f.SQL()
	if want := "(state = ?)"; where != want {
		t.Errorf("SQL = %q, want the convertible half %q", where, want)
	}
	if len(args) != 1 || args[0] != "MERGED" {
		t.Errorf("args = %v, want just the state", args)
	}
	if f.residualSrc != `title.matches("^A ")` {
		t.Errorf("residual = %q, want the regex term", f.residualSrc)
	}

	// And the residual is what actually decides, over rows SQL already
	// narrowed.
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"A config table", true},
		{"Add a config table", false},
	} {
		got, err := f.Keep(&row{Title: tc.title})
		if err != nil {
			t.Fatalf("Keep(%q) returned error: %v", tc.title, err)
		}
		if got != tc.want {
			t.Errorf("Keep(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// TestAFilterNobodyPushedDownStillRuns is the bug this experiment hit first:
// the plan said "ran in SQL", the listing never put the fragment in its query,
// and every row came back unfiltered. A filter that assumes it was used is the
// worst answer available, so taking the SQL is what says it was.
func TestAFilterNobodyPushedDownStillRuns(t *testing.T) {
	f := compile(t, `state == "MERGED"`)

	// No call to SQL(): this stands for a listing with no pushdown wired.
	keep, err := f.Keep(&row{State: sql.NullString{String: "OPEN", Valid: true}})
	if err != nil {
		t.Fatalf("Keep() returned error: %v", err)
	}
	if keep {
		t.Error("a row was kept by a filter nothing ran")
	}
	if !strings.Contains(f.Explain(), "in Go") {
		t.Errorf("Explain() = %q, want it to say Go did the work", f.Explain())
	}

	// Taking the SQL changes the answer, because now the query is the thing
	// that excluded it.
	pushed := compile(t, `state == "MERGED"`)
	pushed.SQL()
	if keep, _ := pushed.Keep(&row{State: sql.NullString{String: "OPEN", Valid: true}}); !keep {
		t.Error("a row was filtered twice: the query already excluded it")
	}
}

// TestNullIsNotEmpty: a column with no value has to read as null rather than
// as "", or `state == ""` would match every unsynced row.
func TestNullIsNotEmpty(t *testing.T) {
	f := compile(t, `state == ""`)

	keep, err := f.Keep(&row{}) // State invalid: NULL
	if err != nil {
		t.Fatalf("Keep() returned error: %v", err)
	}
	if keep {
		t.Error("a NULL column matched the empty string")
	}
}

// TestSQLAndGoDisagreeAboutNull records a divergence rather than asserting a
// fix, because it is the finding this experiment most needs to carry.
//
// SQLite's `state != 'OPEN'` is NULL for a NULL state, and a WHERE that is
// NULL excludes the row. CEL's `null != "OPEN"` is true, so the same filter
// run in Go keeps it. The same expression therefore answers differently
// depending on which half of the plan ran it — which is tolerable for an
// experiment and would not be for a feature: the fix is to wrap a pushed-down
// comparison so NULL follows CEL, e.g. `COALESCE(state != ?, TRUE)`.
func TestSQLAndGoDisagreeAboutNull(t *testing.T) {
	goSide := compile(t, `state != "OPEN"`)
	keep, err := goSide.Keep(&row{}) // NULL state, evaluated in Go
	if err != nil {
		t.Fatalf("Keep() returned error: %v", err)
	}
	if !keep {
		t.Error("CEL no longer treats null != \"OPEN\" as true; the divergence may be gone")
	}

	pushed := compile(t, `state != "OPEN"`)
	where, _ := pushed.SQL()
	if !strings.Contains(where, "!=") {
		t.Fatalf("SQL = %q, want a plain comparison", where)
	}
	if strings.Contains(where, "COALESCE") || strings.Contains(where, "IS NULL") {
		t.Error("the pushed-down form now handles NULL; this test should become an equality check")
	}
}

// TestAJSONTermRunsInGo: cel2sql converts `approvals.exists(a, a == "x")` into
// a json_each subquery whose alias it then compares as a scalar, which SQLite
// rejects with "no such column: a" — SQL that is generated, accepted and
// wrong. Until that is fixed upstream, such a term is held back.
func TestAJSONTermRunsInGo(t *testing.T) {
	f := compile(t, `approvals.exists(a, a == "alice")`)

	if where, _ := f.SQL(); where != "" {
		t.Errorf("a JSON term was pushed down as %q", where)
	}

	for _, tc := range []struct {
		approvals string
		want      bool
	}{
		{`["alice","bob"]`, true},
		{`["bob"]`, false},
		{`[]`, false},
	} {
		got, err := f.Keep(&row{Approvals: tc.approvals})
		if err != nil {
			t.Fatalf("Keep(%s) returned error: %v", tc.approvals, err)
		}
		if got != tc.want {
			t.Errorf("Keep(%s) = %v, want %v", tc.approvals, got, tc.want)
		}
	}
}

// TestAnUnknownColumnIsRefusedWithCELsOwnError: the error carries a caret and
// a position, which is more than an error of ours would say.
func TestAnUnknownColumnIsRefusedWithCELsOwnError(t *testing.T) {
	_, err := Compile(&row{}, `stat == "MERGED"`)
	if err == nil {
		t.Fatal("Compile accepted a column that does not exist")
	}
	if !strings.Contains(err.Error(), "undeclared reference") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

// TestSplittingIsConservative: a splitter that split inside a string or inside
// brackets would change what the filter means. Being unsure has to cost a
// pushdown, never an answer.
func TestSplittingIsConservative(t *testing.T) {
	tests := []struct {
		expr string
		want int
	}{
		{`state == "MERGED"`, 1},
		{`state == "MERGED" && number > 1`, 2},
		{`state == "MERGED" && number > 1 && title != ""`, 3},
		// An or is never split: its sides are not independent filters, and
		// pushing one down would return rows the other excluded.
		{`state == "MERGED" || number > 1`, 1},
		{`(state == "OPEN" || state == "MERGED") && number > 1`, 2},
		// && inside a string is text, not an operator.
		{`title == "a && b"`, 1},
		// && inside a comprehension belongs to the comprehension.
		{`approvals.exists(a, a == "x" && a != "y")`, 1},
	}
	for _, tc := range tests {
		if got := len(conjuncts(tc.expr)); got != tc.want {
			t.Errorf("conjuncts(%q) = %d terms, want %d: %q",
				tc.expr, got, tc.want, conjuncts(tc.expr))
		}
	}
}
