// Package filter compiles a CEL expression over one entity's columns, and
// runs as much of it as it can in SQL.
//
// # Why CEL rather than a filter language of our own
//
// Every listing already knows its own columns and their types — that is what
// the column registry was built for — so the vocabulary of a filter is
// something roz can hand a general expression language rather than something
// it has to invent. CEL brings the parser, the type checker, an error message
// with a caret in it, and a specification somebody else maintains. What it
// does not bring is a way to run against SQLite, which is the whole of the
// work here.
//
// # Three ways to run a filter, and why this uses all three
//
//  1. Push it into the query. `state == "MERGED"` becomes `state = ?`, and
//     SQLite does the work. This is the only version that stays correct in
//     front of a LIMIT, and the only one that does not read every row.
//  2. Evaluate it in Go over the rows already read. Always possible, never
//     pushed down, and wrong the moment a listing grows a limit.
//  3. Split: push down what converts, evaluate the rest in Go. A filter is
//     usually a conjunction, and one unconvertible term should not cost the
//     pushdown of the others.
//
// This does (1) where the whole expression converts, (3) where part of it
// does, and (2) where none of it does — reporting which, because a filter that
// silently read the whole table is a performance question nobody can see.
//
// # What is not here
//
// Joins. `projects.exists(p, p.issue.closed)` needs the filter to know how
// entities relate, which the registry does not yet say. Everything here is one
// record's own columns.
package filter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"

	"github.com/scottlaird/roz/internal/store"
)

// Filter is a compiled expression, already divided into the part SQLite will
// run and the part Go has to.
type Filter struct {
	source string

	// where is the SQL the query should carry, empty when nothing converted.
	where string
	args  []any

	// residual is what SQL could not take, evaluated per row. Nil when the
	// whole expression was pushed down.
	residual    cel.Program
	residualSrc string

	// whole is the entire expression as a program, for a caller that never
	// took the SQL. Compiled always, because whether the query used the
	// pushdown is the caller's fact rather than this one's — and a filter
	// that assumed it had been used would silently return unfiltered rows,
	// which is the worst answer available.
	whole cel.Program

	// pushedDown records that a caller took the SQL and put it in its query.
	pushedDown bool

	// declared is the vocabulary, kept so the Go pass can supply a null for
	// every column rather than only for the ones a record happened to fill.
	declared []string
}

// Compile builds a filter over the columns of blank, which is an empty record
// of the entity being listed.
func Compile(blank any, expr string) (*Filter, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	columns, err := store.ColumnTypes(blank)
	if err != nil {
		return nil, err
	}
	env, err := envFor(columns)
	if err != nil {
		return nil, err
	}

	f := &Filter{source: expr}
	for _, c := range columns {
		f.declared = append(f.declared, c.Name)
	}
	terms := conjuncts(expr)

	probe, err := newNullProbe(columns)
	if err != nil {
		return nil, err
	}
	defer probe.Close()

	var pushed []string
	var residual []string
	for _, term := range terms {
		ast, err := compileTerm(env, term)
		if err != nil {
			return nil, err
		}
		sql, args, err := toSQL(ast, columns)
		if err == nil {
			ok, checkErr := pushable(probe, env, ast, term, columns, sql, args)
			if checkErr != nil {
				return nil, checkErr
			}
			if !ok {
				err = errNotEquivalent
			}
		}
		if err != nil || mentionsJSON(term, columns) {
			// Not a failure: this is the term that has to run in Go.
			//
			// A JSON column is here rather than in the converter's own refusal
			// because the converter does not refuse. It renders
			// `approvals.exists(a, a == "x")` as a json_each subquery whose
			// alias it then compares as a scalar, which SQLite rejects with
			// "no such column: a" — SQL that is generated, accepted, and
			// wrong. Until that is fixed upstream, a term over a JSON column
			// runs in Go, where it is correct.
			residual = append(residual, term)
			continue
		}
		pushed = append(pushed, "("+sql+")")
		f.args = append(f.args, args...)
	}
	f.where = strings.Join(pushed, " AND ")

	if len(residual) > 0 {
		f.residualSrc = strings.Join(residual, " && ")
		ast, err := compileTerm(env, f.residualSrc)
		if err != nil {
			return nil, err
		}
		if f.residual, err = env.Program(ast); err != nil {
			return nil, fmt.Errorf("planning --filter %q: %w", f.residualSrc, err)
		}
	}

	whole, err := compileTerm(env, expr)
	if err != nil {
		return nil, err
	}
	if f.whole, err = env.Program(whole); err != nil {
		return nil, fmt.Errorf("planning --filter %q: %w", expr, err)
	}
	return f, nil
}

// errNotEquivalent marks a term SQLite would answer differently from CEL. Not
// a failure: it is the reason a term runs in Go.
var errNotEquivalent = errors.New("SQL would not answer this the way CEL does")

// pushable reports whether a converted term may be trusted to the query.
//
// The rule is equivalence, checked rather than assumed: a filter is written in
// CEL, so CEL decides what it means, and a fragment that would answer
// differently is an optimisation that changed an answer. NULL is where they
// part company — `state != "OPEN"` keeps a NULL row in CEL and drops it in
// SQL — and it is the only place, because everything else in a converted term
// is a comparison of values both engines have.
func pushable(probe *nullProbe, env *cel.Env, ast *cel.Ast, term string,
	columns []store.ColumnType, fragment string, args []any) (bool, error) {

	if nullableCount(term, columns) == 0 {
		// Nothing in it can be absent, so there is no row shape the two could
		// disagree about.
		return true, nil
	}
	if nullableCount(term, columns) > 1 {
		return false, nil
	}

	program, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("planning --filter %q: %w", term, err)
	}
	return probe.agrees(fragment, args, program)
}

// compileTerm parses and checks one term, reporting CEL's own diagnostic.
//
// CEL's error carries a line, a column and a caret, which is more than an
// error of ours would say, so it is passed through rather than summarised.
func compileTerm(env *cel.Env, expr string) (*cel.Ast, error) {
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("--filter %q: %w", expr, issues.Err())
	}
	return ast, nil
}

// SQL is what the query should carry: a WHERE fragment and its arguments,
// both empty when nothing could be pushed down.
//
// Taking it is a promise to run it. A caller that reads this and then does not
// put it in its query would get every row, and Keep would filter only the part
// SQL was supposed to have handled — so calling this is what tells the filter
// the query did its half.
func (f *Filter) SQL() (string, []any) {
	if f == nil {
		return "", nil
	}
	f.pushedDown = true
	return f.where, f.args
}

// Keep reports whether a row survives the part of the filter SQL could not
// run. True where everything was pushed down, since the query already
// answered.
func (f *Filter) Keep(record any) (bool, error) {
	if f == nil {
		return true, nil
	}
	program, source := f.whole, f.source
	if f.pushedDown {
		if f.residual == nil {
			return true, nil
		}
		program, source = f.residual, f.residualSrc
	}

	values, err := store.ColumnValues(record)
	if err != nil {
		return false, err
	}
	// A column the record does not carry a value for is null rather than
	// absent, so `state != "OPEN"` has something to compare against instead
	// of failing with "no such attribute".
	out, _, err := program.Eval(nullFilled(values, f.columns()))
	if err != nil {
		// A row CEL has no answer for does not match, rather than failing the
		// listing. `number > 5` against a NULL number is the case: CEL calls
		// it an error, SQLite excludes the row, and excluding it is both the
		// agreeing answer and the one somebody wanted.
		return false, nil
	}
	keep, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("--filter %q is not a yes-or-no question; it produced %v",
			source, out.Type())
	}
	return keep, nil
}

// Explain says how the filter was divided, for a caller that wants to show it.
//
// Worth showing rather than keeping: whether a filter reached the query
// decides whether the listing read four rows or forty thousand, and nothing
// else on the screen would say which happened.
func (f *Filter) Explain() string {
	switch {
	case f == nil:
		return ""
	case !f.pushedDown:
		return "filter ran in Go over every row: this listing does not push one down"
	case f.residual == nil:
		return "filter ran in SQL"
	case f.where == "":
		return fmt.Sprintf("filter ran in Go over every row: %s", f.residualSrc)
	default:
		return fmt.Sprintf("filter ran in SQL, except %s, which ran in Go", f.residualSrc)
	}
}

// columns is the vocabulary this filter was compiled against.
func (f *Filter) columns() []string { return f.declared }

// mentionsJSON reports whether a term names one of the record's JSON columns.
//
// By name, which is crude: a string literal containing a column name counts.
// Being wrong here costs a pushdown rather than an answer, and this is a
// demonstration of where the seam falls rather than the seam to keep.
func mentionsJSON(term string, columns []store.ColumnType) bool {
	for _, c := range columns {
		if c.JSON && strings.Contains(term, c.Name) {
			return true
		}
	}
	return false
}

// conjuncts splits an expression at top-level `&&`.
//
// Textual, and deliberately conservative: it splits only outside brackets and
// string literals, and anything it is unsure of stays one term — which costs a
// pushdown rather than changing an answer. Splitting the checked AST instead
// would be exact, but cel-go has no supported way to lift a sub-expression
// back into an *cel.Ast for conversion, and this is an experiment rather than
// the shape to keep.
//
// `||` is never split: the two sides of an or are not independent filters, and
// pushing one down would return rows the other should have excluded.
func conjuncts(expr string) []string {
	var terms []string
	var depth, start int
	var quote rune

	runes := []rune(expr)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case depth == 0 && c == '&' && i+1 < len(runes) && runes[i+1] == '&':
			terms = append(terms, strings.TrimSpace(string(runes[start:i])))
			i++
			start = i + 1
		}
	}
	terms = append(terms, strings.TrimSpace(string(runes[start:])))

	for _, term := range terms {
		if term == "" {
			// An unbalanced expression: let CEL report it, on the whole thing.
			return []string{expr}
		}
	}
	return terms
}
