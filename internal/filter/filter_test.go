package filter

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/scottlaird/roz/internal/store"
)

// row is a record shaped like roz's, without being one: a text column, a
// nullable one, an integer, and a JSON list.
type row struct {
	ID        string         `db:"id" kind:"identity"`
	State     sql.NullString `db:"state" kind:"observed"`
	Title     string         `db:"title"`
	Number    int64          `db:"number"`
	MergedAt  sql.NullString `db:"merged_at" kind:"observed"`
	CreatedAt string         `db:"created_at" kind:"created"`
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

// TestAJSONTermPushesDown, which it did not until SPANDigital/cel2sql#169.
//
// The converter turned `approvals.exists(a, a == "x")` into a json_each
// subquery and then compared the subquery's alias as a scalar — `WHERE a = ?`
// — SQL that was generated, accepted by the converter and rejected by SQLite
// with "no such column: a". The fix writes the iteration variable through the
// dialect; v3.8.9 carries it, and the term now reads `a.value`.
//
// See scottlaird/roz#202.
func TestAJSONTermPushesDown(t *testing.T) {
	f := compile(t, `approvals.exists(a, a == "alice")`)

	where, args := f.SQL()
	if where == "" {
		t.Fatal("a JSON term was held back, and the upstream fix is in")
	}
	if !strings.Contains(where, "json_each") || !strings.Contains(where, "a.value") {
		t.Errorf("SQL = %q, want a json_each comparing a.value", where)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Errorf("args = %v, want the literal as a value", args)
	}
}

// TestTheJSONTermMeansWhatItSays is the assertion the shape gate exists to
// protect: SQL that SQLite accepts is not the same thing as SQL that means
// what the filter said. The bug behind #202 produced SQL that was generated
// and accepted by the converter, and only SQLite refused it.
//
// So the fragment is run against real SQLite rather than pattern-matched.
func TestTheJSONTermMeansWhatItSays(t *testing.T) {
	f := compile(t, `approvals.exists(a, a == "alice")`)
	where, args := f.SQL()
	if where == "" {
		t.Fatal("nothing was pushed down, so there is nothing to run")
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE r (approvals TEXT NOT NULL DEFAULT '[]')`); err != nil {
		t.Fatalf("creating the table: %v", err)
	}

	for _, tt := range []struct {
		approvals string
		want      bool
		why       string
	}{
		{`["alice","bob"]`, true, "the name among others"},
		{`["alice"]`, true, "the name alone"},
		{`["bob"]`, false, "somebody else"},
		// CEL's exists over [] is false and EXISTS over no rows is false, so
		// the two agree here without any special case. These columns are NOT
		// NULL DEFAULT '[]', so there is no third state to disagree about.
		{`[]`, false, "nobody"},
		{`["ALICE"]`, false, "case matters, as it does in CEL"},
		{`["alicia"]`, false, "a prefix is not a member"},
	} {
		t.Run(tt.approvals, func(t *testing.T) {
			if _, err := db.Exec(`DELETE FROM r`); err != nil {
				t.Fatalf("clearing: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO r VALUES (?)`, tt.approvals); err != nil {
				t.Fatalf("inserting: %v", err)
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM r WHERE `+where, args...).Scan(&n); err != nil {
				t.Fatalf("SQLite refused %q: %v", where, err)
			}
			if got := n == 1; got != tt.want {
				t.Errorf("%s: matched = %v, want %v (%s)", tt.approvals, got, tt.want, tt.why)
			}
		})
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

// TestSplittingIsExact: the split is now the language's idea of where the ands
// are, taken off the checked tree and unparsed back to source, rather than a
// scanner's guess at it.
func TestSplittingIsExact(t *testing.T) {
	columns, err := store.ColumnTypes(&row{})
	if err != nil {
		t.Fatal(err)
	}
	env, err := envFor(columns, nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		expr string
		want []string
	}{
		{`state == "MERGED"`, []string{`state == "MERGED"`}},
		{`state == "MERGED" && number > 1`, []string{`state == "MERGED"`, `number > 1`}},
		{
			`state == "MERGED" && number > 1 && title != ""`,
			[]string{`state == "MERGED"`, `number > 1`, `title != ""`},
		},
		// An or is one term: its sides are not independent filters, and
		// pushing one down would return rows the other excluded.
		{`state == "MERGED" || number > 1`, []string{`state == "MERGED" || number > 1`}},
		{
			`(state == "OPEN" || state == "MERGED") && number > 1`,
			[]string{`state == "OPEN" || state == "MERGED"`, `number > 1`},
		},
		// An && inside a string is text, and one inside a comprehension
		// belongs to the comprehension. The old textual splitter got these
		// right by counting quotes and brackets; this one never has to.
		{`title == "a && b"`, []string{`title == "a && b"`}},
		{
			`approvals.exists(a, a == "x" && a != "y")`,
			[]string{`approvals.exists(a, a == "x" && a != "y")`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := conjuncts(env, tc.expr)
			if err != nil {
				t.Fatalf("conjuncts returned error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("conjuncts = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("term %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestAnExpressionThatCostsTooMuchIsAnError, not a row that fails to match.
//
// Treating an exhausted budget as "does not match" would return an answer
// computed from a prefix of the work, which is the one outcome worse than
// being slow.
func TestAnExpressionThatCostsTooMuchIsAnError(t *testing.T) {
	// The predicate is never true, so neither exists() short-circuits and the
	// walk is the full product.
	f, err := Compile(&row{}, `approvals.exists(a, approvals.exists(b, a + b == "no"))`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	// A list big enough that the nested walk passes the budget.
	big := make([]string, 1200)
	for i := range big {
		big[i] = "login"
	}
	encoded, err := json.Marshal(big)
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.Keep(&row{Approvals: string(encoded)})
	if err == nil {
		t.Fatal("an expression over budget answered rather than failing")
	}
	if !strings.Contains(err.Error(), "cost") {
		t.Errorf("error does not say what went wrong: %v", err)
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
		// Both became pushable when the store started opening connections with
		// case_sensitive_like. The converter escapes the literal's own % and _
		// and emits an ESCAPE clause, so a prefix holding a wildcard is a
		// prefix rather than a pattern.
		{`title.startsWith("Fix")`, true, "LIKE is case-sensitive now, as startsWith always was"},
		{`title.endsWith("x")`, true, "the same, from the other end"},
		{`title.startsWith("100%")`, true, "the wildcard in the literal is escaped, not honoured"},
		{`title.matches("^Fix")`, false, "the dialect refuses regexes outright"},
		{`!merged_at`, false, "NOT over a non-boolean is SQLite coercing, not negating"},
		{`number > "5"`, false, "SQLite compares across types by affinity; CEL calls it an error"},
		// Column against column was refused until a join needed it — "a child
		// that outranks its parent" compares two rows' priorities and neither
		// side is a literal. The rules are the literal ones applied to both
		// sides.
		{`state == title`, true, "two columns of one kind, and NULL excludes in both engines"},
		{`state != title`, false, "state may be absent, and != is where the two part company"},
		{`title != id`, true, "neither column can be absent, so there is no row to differ over"},
		{`approvals.exists(a, a == "x")`, true,
			"membership of a JSON array of text, since cel2sql#169 made the SQL valid"},
		{`approvals.exists(a, a != "x")`, false,
			"a different question about emptiness, and one nobody has checked"},
		{`approvals.exists(a, a.startsWith("x"))`, false,
			"the inner comparison is not the one shape that was checked"},
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

// TestStartsWithMatchesCaseInBothEngines is the shape that argued hardest for
// the allow-list, and is allowed now because the reason it was refused went
// away rather than because anybody stopped worrying about it.
//
// SQLite's LIKE was case-insensitive for ASCII, so `title LIKE 'fix%'` matched
// "Fix the thing" in the query where CEL's startsWith did not. The store now
// opens every connection with case_sensitive_like, and the equivalence probe
// opens its scratch database the same way, so this has to agree there before
// it reaches a query at all.
func TestStartsWithMatchesCaseInBothEngines(t *testing.T) {
	f := compile(t, `title.startsWith("fix")`)

	where, _ := f.SQL()
	if where == "" {
		t.Fatal("startsWith did not push down")
	}
	if !strings.Contains(where, "LIKE") {
		t.Errorf("SQL = %q, want a LIKE", where)
	}

	// And in Go it is case-sensitive, which is what the query now matches.
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"fix the thing", true},
		{"Fix the thing", false},
	} {
		got, err := compile(t, `title.startsWith("fix")`).Keep(&row{Title: tc.title})
		if err != nil {
			t.Fatalf("Keep(%q) returned error: %v", tc.title, err)
		}
		if got != tc.want {
			t.Errorf("Keep(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// TestTheClockBecomesALiteral is what lets a filter about time reach the
// query.
//
// `now` is inlined rather than bound, because the allow-list pushes down a
// comparison against a literal and a bound variable is an identifier in the
// tree. Filters about time are the ones people write, so leaving them in Go
// would have left the common case reading every row.
func TestTheClockBecomesALiteral(t *testing.T) {
	f := compile(t, `merged_at < now`)

	where, args := f.SQL()
	if want := "(merged_at < ?)"; where != want {
		t.Fatalf("SQL = %q, want %q", where, want)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want the moment as a parameter", args)
	}
	stamp, ok := args[0].(string)
	if !ok || !strings.HasSuffix(stamp, "Z") {
		t.Errorf("args[0] = %#v, want an ISO-8601 UTC timestamp", args[0])
	}
}

// TestOneFilterHasOneNow: a clock that moved between terms could answer a
// question no instant would.
func TestOneFilterHasOneNow(t *testing.T) {
	f := compile(t, `merged_at < now && created_at < now`)

	_, args := f.SQL()
	if len(args) != 2 {
		t.Fatalf("args = %v, want one per term", args)
	}
	if args[0] != args[1] {
		t.Errorf("the two terms saw different moments: %v and %v", args[0], args[1])
	}
}

// TestOnlyTheIdentifierIsAClock: `now` occurs inside other words and inside
// strings, and only the parser knows which occurrences are the identifier.
// Searching the text would have rewritten all of them.
func TestOnlyTheIdentifierIsAClock(t *testing.T) {
	f := compile(t, `title == "now or never" && merged_at < now`)

	where, args := f.SQL()
	if want := "(title = ?) AND (merged_at < ?)"; where != want {
		t.Fatalf("SQL = %q, want %q", where, want)
	}
	if args[0] != "now or never" {
		t.Errorf("the string literal was rewritten: %#v", args[0])
	}
	if stamp, _ := args[1].(string); !strings.HasSuffix(stamp, "Z") {
		t.Errorf("the identifier was not: %#v", args[1])
	}
}

// TestAColumnNameInAStringIsNotAColumn is what the textual matching got wrong.
//
// `title == "approvals"` mentions a JSON column's name and reads none, so the
// old check held it back from the query for nothing. Resolving through the
// scope asks the tree instead, which knows a literal from an identifier.
func TestAColumnNameInAStringIsNotAColumn(t *testing.T) {
	f := compile(t, `title == "approvals"`)

	where, args := f.SQL()
	if want := "(title = ?)"; where != want {
		t.Errorf("SQL = %q, want %q", where, want)
	}
	if len(args) != 1 || args[0] != "approvals" {
		t.Errorf("args = %v, want the string as a value", args)
	}
}

// TestOnlyTheCheckedJSONShapeIsPushed. The gate is an allow-list because a
// probe can only fail to find a difference, never show there is none — so what
// was not checked runs in Go, which is slower and right.
func TestOnlyTheCheckedJSONShapeIsPushed(t *testing.T) {
	for _, tt := range []struct{ expr, why string }{
		{`approvals.exists(a, a != "alice")`, "!= over an array asks about emptiness too"},
		{`approvals.exists(a, a.contains("ali"))`, "a substring test inside the array"},
		{`approvals.all(a, a == "alice")`, "all over [] is true in CEL and has no EXISTS twin"},
		{`approvals.exists(a, a == title)`, "the other side is a column, not a literal"},
	} {
		t.Run(tt.expr, func(t *testing.T) {
			f := compile(t, tt.expr)
			if where, _ := f.SQL(); where != "" {
				t.Errorf("pushed down as %q, but %s", where, tt.why)
			}
		})
	}
}
