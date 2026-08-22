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
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"

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

	// joins are the relations this entity can be filtered across, for a Go
	// pass that has to resolve one.
	joins *joinEnv
	// loader and ctx are how the Go pass reads a relation. Nil until a caller
	// supplies them; a residual that traverses then says so rather than
	// answering.
	loader Loader
	ctx    context.Context
	// traversesWhole and traversesResidual name the relations each program
	// needs. Two of them, because which one runs depends on whether a caller
	// took the SQL — and asking for a loader the running program does not need
	// would refuse a filter that works.
	traversesWhole    []string
	traversesResidual []string
	// mentions are every name the expression selects, which is what a far row
	// consults to decide which of its own relations to read. Wider than it
	// needs to be — a column name lands here too — and harmless, because it
	// is only ever intersected with an entity's actual relations.
	mentions map[string]bool
	// chains are the terms that follow more than one relation. Kept because
	// they can only be answered in the query: see Keep.
	chains []string
}

// Compile builds a filter over the columns of blank, which is an empty record
// of the entity being listed.
func Compile(blank any, expr string, opts ...Option) (*Filter, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	columns, err := store.ColumnTypes(blank)
	if err != nil {
		return nil, err
	}
	joins, err := newJoinEnv(blank)
	if err != nil {
		return nil, err
	}
	settings := settingsFrom(opts)
	if joins != nil && settings.alias != "" {
		// The name a correlated subquery uses for the row being filtered. It
		// has to be what the listing's own query calls that table: `action
		// list` aliases it `a`, and a subquery correlating to `action.id`
		// would not resolve against a query that never mentions `action`.
		joins.base = settings.alias
	}
	env, err := envFor(columns, joins)
	if err != nil {
		return nil, err
	}

	f := &Filter{source: expr, joins: joins}
	for _, c := range columns {
		f.declared = append(f.declared, c.Name)
	}
	// One moment for the whole filter, before it is split: two terms that
	// disagreed about the time could answer a question no instant would.
	expr, err = withClock(env, expr, time.Now().UTC().Format(timeFormat))
	if err != nil {
		return nil, err
	}
	f.source = expr

	terms, err := conjuncts(env, expr)
	if err != nil {
		return nil, err
	}

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
		sql, args, isChain, err := joinSQL(ast, env, joins, columns)
		// A refusal is not a demotion. errNotAJoin and errNotEquivalent say
		// "somewhere else should run this"; anything else says the filter
		// cannot be answered at all, and returning rows regardless would be a
		// wrong answer rather than a slow one.
		if err != nil && !errors.Is(err, errNotAJoin) && !errors.Is(err, errNotEquivalent) {
			return nil, err
		}
		if err == errNotAJoin {
			scalar := ast
			if settings.alias != "" {
				// Qualified with the listing's alias, so a bare column name
				// cannot be ambiguous in a query that joins another table.
				qualified, qErr := celFor(env,
					qualify(ast.NativeRep().Expr(), settings.alias, columnsByName(columns)),
					ast.NativeRep().SourceInfo())
				if qErr != nil {
					return nil, qErr
				}
				scalar = qualified
			}
			sql, args, err = toSQL(scalar, schemasFor(columns, joins, "", ""))
			if err == nil {
				ok, checkErr := pushable(probe, env, ast, term, columns, sql, args)
				if checkErr != nil {
					return nil, checkErr
				}
				if !ok {
					err = errNotEquivalent
				}
			}
		}
		if err != nil {
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
		if isChain {
			f.chains = append(f.chains, term)
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
		if f.residual, err = programOf(env, ast); err != nil {
			return nil, fmt.Errorf("planning --filter %q: %w", f.residualSrc, err)
		}
		// What this program will have to read. Kept so that "nothing matched"
		// and "nowhere to read from" stop looking alike.
		f.traversesResidual = relationsIn(ast.NativeRep().Expr(), joins)
	}

	whole, err := compileTerm(env, expr)
	if err != nil {
		return nil, err
	}
	if f.whole, err = programOf(env, whole); err != nil {
		return nil, fmt.Errorf("planning --filter %q: %w", expr, err)
	}
	f.traversesWhole = relationsIn(whole.NativeRep().Expr(), joins)
	f.mentions = selectedNames(whole.NativeRep().Expr())
	return f, nil
}

// errNotEquivalent marks a term SQLite would answer differently from CEL. Not
// a failure: it is the reason a term runs in Go.
var errNotEquivalent = errors.New("SQL would not answer this the way CEL does")

// errNotAJoin says a term does not traverse a relation, so the scalar path
// should have it.
var errNotAJoin = errors.New("not a traversal")

// joinSQL renders a term that reaches across a relation, or says it is not one.
//
// The inner predicate goes through the same converter as a top-level one, with
// the far table's columns declared under the iteration variable's name — so
// `a.verb == "merge"` converts to `a.verb = ?`, and aliasing the table `a` in
// the subquery is all it takes to make that valid.
func joinSQL(ast *cel.Ast, env *cel.Env, joins *joinEnv, columns []store.ColumnType) (string, []any, bool, error) {
	if joins == nil {
		return "", nil, false, errNotAJoin
	}
	root := ast.NativeRep().Expr()
	info := ast.NativeRep().SourceInfo()
	top := topFor(joins, columns)

	// A chain — more than one relation — goes through the recursive
	// generator, which renders nested EXISTS and refuses outright if it
	// cannot render the whole thing. Never a fallback to Go: see chain.go for
	// why a false empty is the alternative.
	if chained(root, top, info) {
		sql, args, err := predicateSQL(env, info, root, top)
		if err != nil {
			if errors.Is(err, errNotEquivalent) {
				return "", nil, false, fmt.Errorf(
					"a filter that follows two relations has to run in the query, "+
						"and this one cannot be converted: %w", errChainInGo)
			}
			return "", nil, false, err
		}
		return sql, args, true, nil
	}

	// Anything the generator does not take is still refused outright rather
	// than demoted: a shape that reaches twice and falls through to Go is the
	// false empty this whole path exists to prevent.
	if err := joins.checkNoChains(root, "", ""); err != nil {
		return "", nil, false, err
	}

	x, ok := asExists(ast, root, joins)
	if !ok {
		// No quantifier, but the term may still reach through a to-one
		// relation, which needs no quantifier because there is at most one row
		// on the far side.
		reached := toOneRefs(root, joins)
		switch len(reached) {
		case 0:
			return "", nil, false, errNotAJoin
		case 1:
			if !pushableShape(root, joins.scopeFor(columns, reached[0], reached[0])) {
				return "", nil, false, errNotEquivalent
			}
			sql, args, err := toSQL(ast, schemasFor(columns, joins, "", ""))
			if err != nil {
				return "", nil, false, err
			}
			rendered, params := joins.toOne(reached[0], sql, args)
			return rendered, params, false, nil
		default:
			// Two relations in one term needs two subqueries and a decision
			// about how they compose. Not in this spike.
			return "", nil, false, fmt.Errorf("a filter reaching %d relations at once is not supported yet",
				len(reached))
		}
	}

	// An emptiness check has no predicate to convert: the subquery asks only
	// whether anything is there.
	if x.predicate == nil {
		sql, args := joins.subquery(x, "", nil)
		return sql, args, false, nil
	}

	// A second hop inside the traversal is refused here rather than falling
	// through to Go, where the far row is a map of columns and `a.project`
	// evaluates to an error that reads as "no match" for every row.
	if err := joins.checkNoChains(x.predicate, x.iter, x.relation); err != nil {
		return "", nil, false, err
	}

	// The iteration variable is bound by the comprehension, so it exists
	// nowhere outside it. Compiling the predicate on its own needs it
	// declared, which is what this extension is for.
	scoped, err := env.Extend(cel.Variable(x.iter, cel.DynType))
	if err != nil {
		return "", nil, false, fmt.Errorf("scoping %q: %w", x.iter, err)
	}
	inner, err := celFor(scoped, x.predicate, ast.NativeRep().SourceInfo())
	if err != nil {
		return "", nil, false, err
	}
	// The same allow-list as a top-level term, in the join's namespace. Two
	// things go wrong without it: `a.verb.startsWith("W")` converts to a LIKE
	// that is case-insensitive where CEL is not, and a bare name inside the
	// predicate binds to the inner table in SQL and to the outer row in CEL.
	if !pushableShape(inner.NativeRep().Expr(), joins.scopeFor(columns, x.iter, x.relation)) {
		return "", nil, false, errNotEquivalent
	}
	sql, args, err := toSQL(inner, schemasFor(columns, joins, x.iter, x.relation))
	if err != nil {
		return "", nil, false, err
	}
	rendered, params := joins.subquery(x, sql, args)
	return rendered, params, false, nil
}

// celFor lifts a sub-expression back into an AST the converter will take.
//
// Through the unparser: cel.ExprToString renders any node back to CEL source,
// and compiling that gives a checked AST of its own. Slicing a checked AST
// directly is not supported, and this is the route that is — which also means
// the textual conjunct splitter above could be replaced by an exact one, and
// should be if any of this is kept.
func celFor(env *cel.Env, e celast.Expr, info *celast.SourceInfo) (*cel.Ast, error) {
	source, err := cel.ExprToString(e, info)
	if err != nil {
		return nil, fmt.Errorf("rendering the inner expression: %w", err)
	}
	ast, issues := env.Compile(source)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compiling the inner expression %q: %w", source, issues.Err())
	}
	return ast, nil
}

// pushable reports whether a converted term may be trusted to the query.
//
// Two gates, and the order matters. First the expression has to be one of the
// shapes known to mean the same thing in both engines; then the probe has to
// agree that it still does.
//
// Default deny, because the other way round — convert everything, demote what
// the probe catches — was wrong twice. A probe can only fail to find a
// difference, never show there is none, and both misses were a value nobody
// thought to try: `!merged_at` survived a row of NULLs, and `startsWith` would
// survive still, since SQLite's LIKE is case-insensitive for ASCII and every
// sample string is lower case. An allow-list fails the other way, towards Go,
// which is slower and right.
//
// The probe stays as the second gate rather than being replaced by the first.
// The shapes record what roz believes cel2sql emits; the probe checks that it
// still does, so a change upstream costs a pushdown rather than an answer.
func pushable(probe *nullProbe, env *cel.Env, ast *cel.Ast, term string,
	columns []store.ColumnType, fragment string, args []any) (bool, error) {

	at := topLevel(columnsByName(columns))
	root := ast.NativeRep().Expr()
	if !pushableShape(root, at) {
		return false, nil
	}

	referenced := columnsIn(root, at)

	// More than one column that may be absent has row shapes the samples never
	// build — NULL in the first with a value in the second — so it is not
	// pushed down on a partial check. Rare, and cheap to be wrong about in
	// this direction.
	if nullableCount(referenced) > 1 {
		return false, nil
	}

	program, err := programOf(env, ast)
	if err != nil {
		return false, fmt.Errorf("planning --filter %q: %w", term, err)
	}
	return probe.agrees(fragment, args, program, referenced)
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

// withClock replaces `now` with the moment this filter was compiled.
//
// Substituted into the source rather than bound as a variable, because the
// allow-list pushes down a comparison against a *literal* and a bound variable
// is an identifier in the tree. `snooze_until < now` becomes
// `snooze_until < "2026-08-15T20:00:00.000Z"`, which is a shape SQLite can be
// trusted with — so a filter about time reaches the query rather than reading
// every row.
//
// At the offsets the parser recorded, not by searching the text: `now` occurs
// inside `nowhere` and inside a string literal, and only the parser knows
// which occurrences are the identifier.
//
// cel-go's inlining optimizer would have been the obvious tool and is not: it
// wraps a variable used more than once in a `cel.bind`, which is a macro the
// converter cannot take, so `a < now && b < now` would have stopped pushing
// down exactly when it started being useful.
//
// One moment for the whole filter, fixed when it is compiled. A clock that
// moved between terms could answer a question no instant would.
func withClock(env *cel.Env, expr string, at string) (string, error) {
	ast, issues := env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return "", issues.Err()
	}
	info := ast.NativeRep().SourceInfo()

	var spans []celast.OffsetRange
	var walk func(celast.Expr)
	walk = func(n celast.Expr) {
		if n == nil {
			return
		}
		if n.Kind() == celast.IdentKind && n.AsIdent() == nowVariable {
			if span, ok := info.GetOffsetRange(n.ID()); ok {
				spans = append(spans, span)
			}
		}
		switch n.Kind() {
		case celast.SelectKind:
			walk(n.AsSelect().Operand())
		case celast.CallKind:
			call := n.AsCall()
			walk(call.Target())
			for _, arg := range call.Args() {
				walk(arg)
			}
		case celast.ListKind:
			for _, element := range n.AsList().Elements() {
				walk(element)
			}
		case celast.ComprehensionKind:
			c := n.AsComprehension()
			walk(c.IterRange())
			walk(c.LoopCondition())
			walk(c.LoopStep())
			walk(c.Result())
		}
	}
	walk(ast.NativeRep().Expr())
	if len(spans) == 0 {
		return expr, nil
	}

	// Back to front, so replacing one does not move the next.
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start > spans[j].Start })
	literal := strconv.Quote(at)
	rewritten := expr
	for _, span := range spans {
		if int(span.Start) > len(rewritten) || int(span.Stop) > len(rewritten) {
			return "", fmt.Errorf("the clock does not fit in --filter %q", expr)
		}
		rewritten = rewritten[:span.Start] + literal + rewritten[span.Stop:]
	}
	return rewritten, nil
}

// timeFormat is the store's, because a filter compares against stored
// timestamps as text.
const timeFormat = "2006-01-02T15:04:05.000Z"

// mentionsIdent reports whether an expression names an identifier.
func mentionsIdent(e celast.Expr, name string) bool {
	found := false
	var walk func(celast.Expr)
	walk = func(n celast.Expr) {
		if n == nil || found {
			return
		}
		switch n.Kind() {
		case celast.IdentKind:
			found = found || n.AsIdent() == name
		case celast.SelectKind:
			walk(n.AsSelect().Operand())
		case celast.CallKind:
			call := n.AsCall()
			walk(call.Target())
			for _, arg := range call.Args() {
				walk(arg)
			}
		case celast.ListKind:
			for _, element := range n.AsList().Elements() {
				walk(element)
			}
		case celast.ComprehensionKind:
			c := n.AsComprehension()
			walk(c.IterRange())
			walk(c.LoopCondition())
			walk(c.LoopStep())
			walk(c.Result())
		}
	}
	walk(e)
	return found
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
	program, source, needs := f.whole, f.source, f.traversesWhole
	if f.pushedDown {
		if f.residual == nil {
			return true, nil
		}
		program, source, needs = f.residual, f.residualSrc, f.traversesResidual
	}

	// A chain used to be refused here: the far row was a map of columns, so
	// the second hop was a missing key, which CEL reports as an error and the
	// switch below reads as "does not match" — for every row. A far row now
	// follows its own relations (see rowActivation.follow), so this answers
	// rather than refusing. It is N×M where the query would have been one
	// statement, which --explain-filter is how anybody notices.
	if !f.pushedDown && len(f.chains) > 0 && f.loader == nil {
		return false, fmt.Errorf(
			"--filter %q follows two relations, and this listing gave it nowhere "+
				"to read the far side from", strings.Join(f.chains, " && "))
	}

	if len(needs) > 0 && f.loader == nil {
		// Evaluating would fail to resolve the name, and a failure to resolve
		// is swallowed below as "this row does not match" — which would turn a
		// wiring mistake into an empty listing nobody can tell from a correct
		// one.
		return false, fmt.Errorf(
			"--filter %q reaches %s, and this listing has nowhere to read it from",
			source, strings.Join(needs, ", "))
	}

	values, err := store.ColumnValues(record)
	if err != nil {
		return false, err
	}
	// A column the record does not carry a value for is null rather than
	// absent, so `state != "OPEN"` has something to compare against instead
	// of failing with "no such attribute".
	filled := nullFilled(values, f.columns())

	// Relations are resolved on demand: an expression that never mentions one
	// costs no query, and one that does costs a query for that row alone.
	var id string
	if value, ok := values["id"].(string); ok {
		id = value
	}
	activation := &rowActivation{
		ctx: f.ctx, values: filled, joins: f.joins, loader: f.loader, id: id,
		mentions: f.mentions,
	}
	if activation.ctx == nil {
		activation.ctx = context.Background()
	}

	out, details, err := program.Eval(activation)
	if err != nil {
		// Two failures arrive the same way and mean opposite things.
		//
		// Running out of budget is a filter nobody can trust: treating that
		// row as "does not match" would return an answer computed from a
		// prefix of the work.
		if details != nil && details.ActualCost() != nil && *details.ActualCost() >= maxCost {
			return false, fmt.Errorf(
				"--filter %q cost more than %d to evaluate for one row; simplify it",
				source, maxCost)
		}
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

// RunsInGo reports that some of the filter did not reach the query.
//
// For a caller that cannot pay for the Go pass — the page renders on every
// request and cannot read every row to answer one question — knowing that a
// filter converted *whole* is different from knowing it converted at all.
func (f *Filter) RunsInGo() bool {
	return f != nil && f.residual != nil
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

// conjuncts splits an expression at top-level `&&`, exactly.
//
// Off the checked tree rather than the text. The first version of this walked
// the string counting brackets and quotes, on the belief that cel-go had no
// supported way to lift a sub-expression back out of an AST. It has:
// cel.ExprToString unparses any node to CEL source, which compiles again on
// its own. So the split is now the language's idea of where the ands are
// rather than a scanner's.
//
// `||` is never split. The two sides of an or are not independent filters, and
// pushing one down would return rows the other should have excluded.
func conjuncts(env *cel.Env, expr string) ([]string, error) {
	ast, err := compileTerm(env, expr)
	if err != nil {
		return nil, err
	}

	var terms []string
	var split func(celast.Expr) error
	split = func(n celast.Expr) error {
		if n != nil && n.Kind() == celast.CallKind {
			call := n.AsCall()
			if call.FunctionName() == operators.LogicalAnd && len(call.Args()) == 2 {
				if err := split(call.Args()[0]); err != nil {
					return err
				}
				return split(call.Args()[1])
			}
		}
		source, err := cel.ExprToString(n, ast.NativeRep().SourceInfo())
		if err != nil {
			return fmt.Errorf("rendering a term of --filter %q: %w", expr, err)
		}
		terms = append(terms, source)
		return nil
	}
	if err := split(ast.NativeRep().Expr()); err != nil {
		return nil, err
	}
	return terms, nil
}

// maxCost bounds one row's evaluation.
//
// CEL's cost units are abstract: a comparison is a handful, and walking a
// relation is one per element plus the predicate. A million is far above
// anything a filter over a personal queue does and far below anything that
// takes visible time, so it is a guard against a pathological expression
// rather than a budget anybody should feel.
//
// It matters more than the localhost threat model suggests. A saved view is
// evaluated per row per render, so an expression that is merely slow becomes a
// page that does not come back — and the person who wrote it is the person
// waiting for it.
const maxCost = 1_000_000

// programOf plans an expression, bounded and counted.
//
// Counted as well as bounded because the two failures have to be told apart: a
// row CEL cannot answer for does not match, while a row that ran out of budget
// is a filter nobody can trust, and both arrive as an error from Eval.
func programOf(env *cel.Env, ast *cel.Ast) (cel.Program, error) {
	return env.Program(ast, cel.CostLimit(maxCost), cel.EvalOptions(cel.OptTrackCost))
}

// An Option adjusts how a filter is compiled.
type Option func(*settings)

type settings struct {
	// alias is what the listing's own query calls the table being filtered.
	alias string
}

func settingsFrom(opts []Option) settings {
	var s settings
	for _, opt := range opts {
		opt(&s)
	}
	return s
}

// WithBaseAlias names the row being filtered as the listing's query does.
//
// Every correlated subquery a traversal generates has to point back at the
// outer row, and it points at it by name. Where a listing selects `FROM action
// a`, that name is `a`, and a filter compiled without knowing so would emit
// `action.id` against a query in which nothing is called `action` — SQL that
// fails at run time rather than a filter that runs somewhere else.
//
// Only the qualifier moves. What the filter is written in stays the entity's
// own vocabulary, because that is what somebody types.
func WithBaseAlias(alias string) Option {
	return func(s *settings) { s.alias = alias }
}
