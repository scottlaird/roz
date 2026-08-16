package filter

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"

	// The same pure-Go driver the store uses. Opened here only against
	// :memory:, to ask SQLite what a fragment means rather than to hold data.
	_ "modernc.org/sqlite"

	"github.com/scottlaird/roz/internal/store"
)

// The contract, decided here rather than left to whichever engine ran:
//
// CEL's semantics are the answer, and pushdown is an optimisation that may not
// change one. A filter is written in CEL, so `state != "OPEN"` means what CEL
// says it means — true for a row whose state is NULL — whether the query or Go
// worked it out. SQL's three-valued logic is the accelerator's business, not
// the reader's.
//
// That leaves one gap CEL has no answer for: `number > 5` against a NULL
// number is not true or false in CEL, it is an error. Erroring the whole
// listing because one row has a NULL would be worse than any answer, so a row
// whose evaluation fails does not match — which is also what SQLite does with
// it, so the two agree there for free.

// nullProbe asks both engines the same question about a row of NULLs, so a
// pushdown can be allowed on evidence rather than on reasoning about which SQL
// operators propagate NULL.
//
// Reasoning would have been wrong twice already: `merged_at == null` becomes
// `merged_at IS NULL`, which is true rather than NULL, and `state == "MERGED"`
// is NULL in SQL and false in CEL — the same exclusion by two different
// routes. Asking is cheap and does not have a list of exceptions to maintain.
type nullProbe struct {
	db      *sql.DB
	columns []store.ColumnType
}

func newNullProbe(columns []store.ColumnType) (*nullProbe, error) {
	// The same LIKE the store uses. A probe that answered LIKE differently
	// from the database it is standing in for would certify an equivalence
	// that does not hold where it matters.
	db, err := sql.Open("sqlite", "file::memory:?_pragma=case_sensitive_like(1)")
	if err != nil {
		return nil, fmt.Errorf("opening a scratch database to check the filter: %w", err)
	}
	return &nullProbe{db: db, columns: columns}, nil
}

func (p *nullProbe) Close() error { return p.db.Close() }

// agrees reports whether the SQL fragment and the CEL program answer the same
// way for every sample row.
//
// A term they disagree about is not pushed down: it runs in Go, where CEL
// decides, and the answer stops depending on which half of the plan saw the
// row.
//
// More than one row, and the reason is a case the all-NULL row alone let
// through. `!merged_at` converts to `NOT merged_at`, which in SQLite means
// "coerces to zero" — true of the text '0' and of anything non-numeric — while
// CEL calls `!"abc"` an error and matches nothing. Both answer no to a row of
// NULLs, so a single probe called them equivalent and pushed down a fragment
// that means something else entirely.
//
// The samples are not a proof. They are a handful of values per column chosen
// because they are where SQLite's type coercion and CEL's type checking part
// company: empty against absent, a numeric-looking string against a word,
// zero against one. Being unsure costs a pushdown.
func (p *nullProbe) agrees(fragment string, args []any, program cel.Program, referenced []store.ColumnType) (bool, error) {
	for _, row := range p.samples(referenced) {
		inSQL, err := p.sqlAnswer(fragment, args, row)
		if err != nil {
			return false, err
		}
		if inSQL != celAnswer(program, p.celRow(row)) {
			return false, nil
		}
	}
	return true, nil
}

// samples are the rows to compare on: a base row, and then each referenced
// column in turn given each value worth trying.
//
// A column the record declares non-nullable is never NULL in the base row.
// Nulling one builds a row the database cannot produce, and rejecting a
// pushdown because the two engines disagree about an impossible row is a cost
// with nothing bought — `title != "x"` was demoted by exactly that until the
// base row stopped inventing a NULL title.
func (p *nullProbe) samples(referenced []store.ColumnType) []map[string]any {
	base := map[string]any{}
	for _, c := range p.columns {
		if c.Nullable {
			continue
		}
		base[c.Name] = samplesFor(c.Kind)[0]
	}

	rows := []map[string]any{base}
	for _, c := range referenced {
		for _, value := range samplesFor(c.Kind) {
			row := make(map[string]any, len(base)+1)
			for name, v := range base {
				row[name] = v
			}
			row[c.Name] = value
			rows = append(rows, row)
		}
	}
	return rows
}

// samplesFor is where the two engines are most likely to differ, by kind.
func samplesFor(kind string) []any {
	switch kind {
	case "integer":
		return []any{int64(0), int64(1), int64(-1)}
	case "boolean":
		return []any{false, true}
	case "real":
		return []any{0.0, 1.5}
	default:
		// "" and "0" are the values SQLite reads as false and CEL reads as a
		// string; "abc" is the one that coerces to zero without looking like
		// it; a timestamp is what these columns actually hold.
		return []any{"", "0", "abc", "2026-08-15T10:00:00Z"}
	}
}

// celRow turns a sample into an activation, with a null for every column the
// sample did not set.
func (p *nullProbe) celRow(sample map[string]any) map[string]any {
	names := make([]string, 0, len(p.columns))
	for _, c := range p.columns {
		names = append(names, c.Name)
	}
	return nullFilled(sample, names)
}

// sqlAnswer evaluates the fragment against one sample row, with every column
// the sample did not set left NULL.
//
// A fragment that comes out NULL is "does not match": a WHERE clause that is
// neither true nor false excludes the row, which is the answer a listing would
// have seen.
func (p *nullProbe) sqlAnswer(fragment string, args []any, sample map[string]any) (bool, error) {
	names := make([]string, 0, len(p.columns))
	values := make([]any, 0, len(p.columns))
	for _, c := range p.columns {
		if value, ok := sample[c.Name]; ok {
			names = append(names, "? AS "+quoteIdent(c.Name))
			values = append(values, value)
			continue
		}
		names = append(names, "NULL AS "+quoteIdent(c.Name))
	}
	// The fragment appears first in the text, so its placeholders bind first
	// — SQLite numbers them by position in the statement, not by which
	// subquery they sit in.
	query := fmt.Sprintf("SELECT (%s) FROM (SELECT %s)", fragment, strings.Join(names, ", "))

	var answer sql.NullBool
	if err := p.db.QueryRow(query, append(append([]any{}, args...), values...)...).Scan(&answer); err != nil {
		// A fragment SQLite will not even parse is not a pushdown worth
		// having, whatever it would have meant.
		return false, nil
	}
	return answer.Valid && answer.Bool, nil
}

// celAnswer runs a program, treating anything that is not a plain yes as a no.
//
// An error is the case that matters: `null > 5` has no answer in CEL, and the
// row it came from is one SQLite would have excluded, so excluding it is both
// the agreeing answer and the useful one.
func celAnswer(program cel.Program, row map[string]any) bool {
	out, _, err := program.Eval(row)
	if err != nil {
		return false
	}
	answer, ok := out.Value().(bool)
	return ok && answer
}

// quoteIdent wraps a column name for the scratch query. The names come from
// struct tags rather than from anybody's typing, but the quoting is free and
// the habit is worth keeping.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// nullableCount is how many of a term's columns may be absent.
//
// A term over one nullable column is settled by the probe: there is exactly
// one row shape where the engines could differ, and it was just tried. A term
// over two is not — NULL in the first and a value in the second is a shape the
// probe never sees — so it goes to Go rather than being pushed down on a
// partial check. Rare, and cheap to be wrong about in this direction.
func nullableCount(referenced []store.ColumnType) int {
	var n int
	for _, c := range referenced {
		if c.Nullable {
			n++
		}
	}
	return n
}

// hasJSON reports whether a term reads a JSON column.
//
// cel2sql converts `approvals.exists(a, a == "x")` into a json_each subquery
// and then compares the alias as a scalar, which SQLite rejects with "no such
// column: a" — SQL that is generated, accepted and wrong. Until that is fixed
// upstream, such a term runs in Go, where it is correct.
func hasJSON(referenced []store.ColumnType) bool {
	for _, c := range referenced {
		if c.JSON {
			return true
		}
	}
	return false
}
