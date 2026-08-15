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

// TestTheEnginesAgreeAboutNull is the fix for what this experiment found.
//
// The contract is CEL's: a filter is written in CEL, so CEL decides what it
// means, and pushdown is an optimisation that may not change an answer. A term
// SQLite would answer differently is not pushed down — it runs in Go, and the
// result stops depending on which half of the plan saw the row.
func TestTheEnginesAgreeAboutNull(t *testing.T) {
	tests := []struct {
		expr     string
		pushed   bool
		keepNull bool
		why      string
	}{
		{
			expr: `state != "OPEN"`, pushed: false, keepNull: true,
			why: "SQL drops a NULL state here and CEL keeps it, so SQL does not get to answer",
		},
		{
			expr: `state == "MERGED"`, pushed: true, keepNull: false,
			why: "NULL in SQL, false in CEL: the same exclusion by two routes",
		},
		{
			expr: `merged_at == null`, pushed: true, keepNull: true,
			why: "IS NULL is true rather than NULL, which is what CEL says too",
		},
		{
			expr: `merged_at != null`, pushed: true, keepNull: false,
			why: "IS NOT NULL is false, and null != null is false",
		},
		{
			expr: `number > 5`, pushed: true, keepNull: false,
			why: "number is not nullable, so no row shape can differ",
		},
		{
			// Not nullable, so an absent value is "" rather than null, and
			// "" != "x" is a plain true in both engines.
			expr: `title != "x"`, pushed: true, keepNull: true,
			why: "title is not nullable: its zero value is a value",
		},
		{
			// Split: the second term is pushed down, the first is not, and
			// the whole expression is still false for an all-NULL row.
			expr: `state != "OPEN" && merged_at != null`, pushed: true, keepNull: false,
			why: "the demoted term does not stop the other being pushed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			f := compile(t, tc.expr)
			where, _ := f.SQL()
			if pushed := where != ""; pushed != tc.pushed {
				t.Errorf("pushed down = %v, want %v (%s): SQL was %q",
					pushed, tc.pushed, tc.why, where)
			}

			// Whichever way it was planned, the answer for a NULL row is the
			// one CEL gives.
			plain := compile(t, tc.expr)
			got, err := plain.Keep(&row{})
			if err != nil {
				t.Fatalf("Keep() returned error: %v", err)
			}
			if got != tc.keepNull {
				t.Errorf("Keep(null row) = %v, want %v (%s)", got, tc.keepNull, tc.why)
			}
		})
	}
}

// TestADemotedTermDoesNotCostTheOthers: one term SQL would answer differently
// is a reason to run that term in Go, not to give up on the query.
func TestADemotedTermDoesNotCostTheOthers(t *testing.T) {
	f := compile(t, `state != "OPEN" && merged_at != null`)

	where, _ := f.SQL()
	if want := "(merged_at IS NOT NULL)"; where != want {
		t.Errorf("SQL = %q, want only the equivalent term %q", where, want)
	}
	if f.residualSrc != `state != "OPEN"` {
		t.Errorf("residual = %q, want the term SQL would have answered differently", f.residualSrc)
	}
}

// TestTwoNullableColumnsAreNotPushedDown: the probe tries one row shape, and a
// term over two nullable columns has shapes it never sees — NULL in one and a
// value in the other. Being wrong in this direction costs a pushdown.
func TestTwoNullableColumnsAreNotPushedDown(t *testing.T) {
	f := compile(t, `state != merged_at`)
	if where, _ := f.SQL(); where != "" {
		t.Errorf("a term over two nullable columns was pushed down as %q", where)
	}
}

// TestARowCELCannotAnswerDoesNotMatch: `number > 5` against a NULL number is
// an error in CEL rather than a yes or a no. Failing the whole listing over
// one such row would be worse than any answer, and SQLite excludes it, so
// excluding it is both the agreeing answer and the useful one.
func TestARowCELCannotAnswerDoesNotMatch(t *testing.T) {
	f := compile(t, `merged_at > "2026-01-01"`)

	keep, err := f.Keep(&row{}) // merged_at is NULL
	if err != nil {
		t.Fatalf("Keep() returned error rather than excluding the row: %v", err)
	}
	if keep {
		t.Error("a row CEL could not answer for was kept")
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

// TestCoercionIsCaughtToo is the case one probe row missed.
//
// `!merged_at` converts to `NOT merged_at`, which SQLite reads as "coerces to
// zero" — true of the text '0', and of any word, since a non-numeric string
// coerces to 0. CEL calls `!"abc"` an error and matches nothing. Both answer
// no to a row of NULLs, so a single all-NULL probe called them equivalent and
// pushed down a fragment that means something else entirely.
func TestCoercionIsCaughtToo(t *testing.T) {
	f := compile(t, `!merged_at`)

	if where, _ := f.SQL(); where != "" {
		t.Errorf("a coercing fragment was pushed down as %q", where)
	}

	// And what it means is CEL's answer, which is that it matches nothing:
	// `!` wants a bool and a timestamp column never holds one.
	for _, r := range []*row{
		{},
		{MergedAt: sql.NullString{String: "0", Valid: true}},
		{MergedAt: sql.NullString{String: "2026-08-15", Valid: true}},
	} {
		keep, err := f.Keep(r)
		if err != nil {
			t.Fatalf("Keep() returned error: %v", err)
		}
		if keep {
			t.Errorf("Keep(%+v) = true; ! wants a bool", r.MergedAt)
		}
	}
}

// TestTheWayToAskForNullIsCheap: `merged_at == null` is the expression that
// says it, and it converts to IS NULL — which an index can serve, and which
// roz's own partial indexes are built on.
func TestTheWayToAskForNullIsCheap(t *testing.T) {
	f := compile(t, `merged_at == null`)

	where, args := f.SQL()
	if want := "(merged_at IS NULL)"; where != want {
		t.Errorf("SQL = %q, want %q", where, want)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want none: IS NULL takes no value", args)
	}

	keep, err := f.Keep(&row{})
	if err != nil {
		t.Fatalf("Keep() returned error: %v", err)
	}
	if !keep {
		t.Error("the null row did not match merged_at == null")
	}
}

// TestTheAllowListDecidesWhatIsTried is the flip: a shape has to be known
// equivalent before it is converted at all, rather than converted and then
// checked. What the probe can do is fail to find a difference; what it cannot
// do is show there is none.
func TestTheAllowListDecidesWhatIsTried(t *testing.T) {
	tests := []struct {
		expr   string
		pushed bool
		why    string
	}{
		{`state == "MERGED"`, true, "a column against a literal of its own type"},
		{`state == "CLOSED" || state == "MERGED"`, true, "an or of two allowed comparisons"},
		{`state == "MERGED" && number > 5`, true, "an and of two allowed comparisons"},
		{`merged_at == null`, true, "IS NULL, which both engines read the same way"},
		{`merged_at != null`, true, "IS NOT NULL, likewise"},
		{`number > 5`, true, "an ordered comparison against its own type"},
		{`title.contains("queue")`, true, "INSTR, which is case-sensitive as CEL is"},
		{`title != "x"`, true, "a column that cannot be absent has no NULL row to differ over"},

		{`state != "MERGED"`, false, "SQL drops a NULL state where CEL keeps it"},
		{`title.startsWith("Fix")`, false, "LIKE is case-insensitive for ASCII and startsWith is not"},
		{`title.endsWith("x")`, false, "the same, from the other end"},
		{`title.matches("^Fix")`, false, "the dialect refuses regexes outright"},
		{`!merged_at`, false, "NOT over a non-boolean is SQLite coercing, not negating"},
		{`number > "5"`, false, "SQLite compares across types by affinity; CEL calls it an error"},
		{`approvals.exists(a, a == "alice")`, false, "a JSON column is not a scalar"},
		// Column against column was refused until a join needed it — "a child
		// that outranks its parent" compares two rows' priorities and neither
		// side is a literal. The rules are the literal ones applied to both
		// sides.
		{`state == title`, true, "two columns of one kind, and NULL excludes in both engines"},
		{`state != title`, false, "state may be absent, and != is where the two part company"},
		{`title != id`, true, "neither column can be absent, so there is no row to differ over"},
	}

	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			f := compile(t, tc.expr)
			where, _ := f.SQL()
			if pushed := where != ""; pushed != tc.pushed {
				t.Errorf("pushed down = %v, want %v (%s): SQL was %q",
					pushed, tc.pushed, tc.why, where)
			}
		})
	}
}

// TestStartsWithWouldHaveBeenWrong is the case that argues for the flip.
//
// The sample rows would not have caught it: SQLite's LIKE is case-insensitive
// for ASCII, so `title LIKE 'fix%'` matches "Fix the thing" while CEL's
// startsWith does not — and every string the probe tries is lower case, so
// both engines agree on every one of them. The shape is refused because
// somebody read what LIKE does, not because a sample found it.
func TestStartsWithWouldHaveBeenWrong(t *testing.T) {
	f := compile(t, `title.startsWith("fix")`)

	if where, _ := f.SQL(); where != "" {
		t.Fatalf("startsWith was pushed down as %q", where)
	}

	// And in Go it means what CEL says, which is case-sensitive.
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"fix the thing", true},
		{"Fix the thing", false},
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
