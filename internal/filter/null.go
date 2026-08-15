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
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("opening a scratch database to check the filter: %w", err)
	}
	return &nullProbe{db: db, columns: columns}, nil
}

func (p *nullProbe) Close() error { return p.db.Close() }

// agrees reports whether the SQL fragment and the CEL program answer the same
// way for a row whose columns are all NULL.
//
// A term they disagree about is not pushed down: it runs in Go, where CEL
// decides, and the answer stops depending on which half of the plan saw the
// row.
func (p *nullProbe) agrees(fragment string, args []any, program cel.Program) (bool, error) {
	inSQL, err := p.sqlAnswer(fragment, args)
	if err != nil {
		return false, err
	}
	return inSQL == celAnswer(program, p.nullRow()), nil
}

// sqlAnswer evaluates the fragment against one all-NULL row.
//
// A fragment that comes out NULL is "does not match": a WHERE clause that is
// neither true nor false excludes the row, which is the answer a listing would
// have seen.
func (p *nullProbe) sqlAnswer(fragment string, args []any) (bool, error) {
	names := make([]string, 0, len(p.columns))
	for _, c := range p.columns {
		names = append(names, "NULL AS "+quoteIdent(c.Name))
	}
	query := fmt.Sprintf("SELECT (%s) FROM (SELECT %s)", fragment, strings.Join(names, ", "))

	var answer sql.NullBool
	if err := p.db.QueryRow(query, args...).Scan(&answer); err != nil {
		// A fragment SQLite will not even parse is not a pushdown worth
		// having, whatever it would have meant.
		return false, nil
	}
	return answer.Valid && answer.Bool, nil
}

// nullRow is the same row as the CEL side sees it.
func (p *nullProbe) nullRow() map[string]any {
	names := make([]string, 0, len(p.columns))
	for _, c := range p.columns {
		names = append(names, c.Name)
	}
	return nullFilled(nil, names)
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
func nullableCount(term string, columns []store.ColumnType) int {
	var n int
	for _, c := range columns {
		if c.Nullable && strings.Contains(term, c.Name) {
			n++
		}
	}
	return n
}
