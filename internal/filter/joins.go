package filter

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"

	"github.com/scottlaird/roz/internal/store"
)

// Joins are correlated subqueries, and that is the whole trick.
//
// Every question worth asking across a relation turns out to be existential —
// "projects with a waiting action", "projects with no issues", "projects a
// child outranks" — and an EXISTS answers all of them without a join in the
// SELECT, without duplicate rows, and without a DISTINCT to undo them.
//
// It is also better behaved than the scalar filters. EXISTS is never NULL: it
// is true or false and nothing else, and CEL's exists() over an empty list is
// false for the same reason. So the divergence that pushed `state != "OPEN"`
// into Go does not arise at the join itself. Only the predicate inside it
// plays by the scalar rules, and those already have an allow-list.
//
// The inner predicate is converted by the same machinery as a top-level one,
// with the far table aliased as the iteration variable. `actions.exists(a,
// a.verb == "merge")` becomes a subquery over `action a`, and cel2sql renders
// `a.verb` as exactly that — the CEL name and the SQL alias are the same
// string, so nothing has to be rewritten.
//
// # Naming the outer row
//
// Inside a join, a reference to the row being filtered has to say which row it
// means: SQLite resolves a bare column name against the innermost FROM, so in
// `children.exists(c, c.priority < priority)` the bare `priority` binds to the
// child and the filter quietly matches nothing. The outer row is therefore
// named by its table — `children.exists(c, c.priority < project.priority)` —
// which is unambiguous and reads better than the alternative.

// joinEnv describes one entity's traversable connections to the compiler.
type joinEnv struct {
	// base is the table being filtered, and the name an outer reference uses.
	base string
	// joins are the relations, by the name a filter writes.
	joins map[string]store.Join
	// columns are the far entity's columns, by relation name.
	columns map[string][]store.ColumnType
}

func newJoinEnv(blank any) (*joinEnv, error) {
	base, err := store.TableOf(blank)
	if err != nil {
		// Not one of roz's entities — a row a listing assembled, or a test
		// fixture. It has columns and no connections, which is a filter
		// vocabulary in its own right rather than an error.
		return nil, nil
	}
	e := &joinEnv{
		base:    base,
		joins:   store.Joins(blank),
		columns: map[string][]store.ColumnType{},
	}
	for name, join := range e.joins {
		if join.Blank == nil {
			continue
		}
		columns, err := store.ColumnTypes(join.Blank())
		if err != nil {
			return nil, err
		}
		e.columns[name] = columns
	}
	return e, nil
}

// declare adds a variable per relation, plus the outer row under its table
// name.
//
// Dyn for the same reason the columns are: a relation may be absent — a
// project with no parent — and only Dyn can hold a null beside a record.
func (e *joinEnv) declare() []cel.EnvOption {
	opts := []cel.EnvOption{cel.Variable(e.base, cel.DynType)}
	for name, join := range e.joins {
		if join.Kind == store.ToMany {
			opts = append(opts, cel.Variable(name, cel.ListType(cel.DynType)))
			continue
		}
		opts = append(opts, cel.Variable(name, cel.DynType))
	}
	return opts
}

// exists is the shape of a quantified traversal, once recognised.
type exists struct {
	// relation is the connection being walked.
	relation string
	// iter is the variable the filter bound each row to, and the alias the
	// subquery gives the far table.
	iter string
	// predicate is what has to hold of one far row.
	predicate celast.Expr
	// negated says the filter asked for none rather than some.
	negated bool
}

// asExists recognises `rel.exists(v, pred)`, `rel.size() == 0` and their
// negations, from the macro call the parser recorded.
//
// Read from the macro call rather than from the comprehension it expands to:
// the expansion is an accumulator, a loop step and a result, and matching that
// shape would be matching cel-go's implementation rather than the language.
func asExists(a *cel.Ast, e celast.Expr, env *joinEnv) (exists, bool) {
	info := a.NativeRep().SourceInfo()

	// A negation wraps whatever is under it.
	if call, ok := asCall(e, operators.LogicalNot); ok {
		inner, found := asExists(a, call.Args()[0], env)
		if !found {
			return exists{}, false
		}
		inner.negated = !inner.negated
		return inner, true
	}

	// `rel.size() == 0` and `size(rel) == 0` ask whether the relation is
	// empty, which is a NOT EXISTS. Only against zero: a filter that counts is
	// a different question, and one this does not answer.
	if call, ok := asCall(e, operators.Equals); ok {
		if rel, empty := asEmptyCheck(call.Args(), env); empty {
			return exists{relation: rel, iter: "x", negated: true}, true
		}
	}

	macro, found := info.GetMacroCall(e.ID())
	if !found || macro.Kind() != celast.CallKind {
		return exists{}, false
	}
	call := macro.AsCall()
	if call.FunctionName() != "exists" || !call.IsMemberFunction() || len(call.Args()) != 2 {
		return exists{}, false
	}
	target := call.Target()
	if target == nil || target.Kind() != celast.IdentKind {
		return exists{}, false
	}
	name := target.AsIdent()
	if join, ok := env.joins[name]; !ok || join.Kind != store.ToMany {
		return exists{}, false
	}
	iter := call.Args()[0]
	if iter.Kind() != celast.IdentKind {
		return exists{}, false
	}
	return exists{relation: name, iter: iter.AsIdent(), predicate: call.Args()[1]}, true
}

// asEmptyCheck recognises a size against zero, in either order.
func asEmptyCheck(args []celast.Expr, env *joinEnv) (string, bool) {
	if len(args) != 2 {
		return "", false
	}
	for _, order := range [][2]celast.Expr{{args[0], args[1]}, {args[1], args[0]}} {
		if literalKind(order[1]) != "integer" || order[1].AsLiteral().Value() != int64(0) {
			continue
		}
		call, ok := asCall(order[0], "size")
		if !ok {
			continue
		}
		var target celast.Expr
		if call.IsMemberFunction() {
			target = call.Target()
		} else if len(call.Args()) == 1 {
			target = call.Args()[0]
		}
		if target == nil || target.Kind() != celast.IdentKind {
			continue
		}
		if join, ok := env.joins[target.AsIdent()]; ok && join.Kind == store.ToMany {
			return target.AsIdent(), true
		}
	}
	return "", false
}

// asCall narrows an expression to a call of one function.
func asCall(e celast.Expr, name string) (celast.CallExpr, bool) {
	if e == nil || e.Kind() != celast.CallKind {
		return nil, false
	}
	call := e.AsCall()
	if call.FunctionName() != name {
		return nil, false
	}
	return call, true
}

// toOneRefs names the to-one relations a term reaches through.
//
// A field access on one — `parent.priority` — is the whole shape: there is no
// quantifier, because there is at most one row on the far side.
func toOneRefs(e celast.Expr, env *joinEnv) []string {
	seen := map[string]bool{}
	var walk func(celast.Expr)
	walk = func(n celast.Expr) {
		if n == nil {
			return
		}
		switch n.Kind() {
		case celast.SelectKind:
			operand := n.AsSelect().Operand()
			if operand != nil && operand.Kind() == celast.IdentKind {
				name := operand.AsIdent()
				if join, ok := env.joins[name]; ok && join.Kind == store.ToOne {
					seen[name] = true
				}
			}
			walk(operand)
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
		}
	}
	walk(e)

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	return names
}

// scopeFor is the namespace a join predicate is checked in: the far row under
// the iteration variable, the filtered row under its table name, and no bare
// names at all.
func (e *joinEnv) scopeFor(columns []store.ColumnType, iter, relation string) scope {
	return scope{
		base:     columnsByName(columns),
		baseName: e.base,
		iter:     iter,
		far:      columnsByName(e.columns[relation]),
	}
}

// relationsIn names every relation an expression reaches for.
//
// Used to tell two silences apart: a filter that matched nothing, and a filter
// that could not be evaluated because nobody gave it anywhere to read from.
func relationsIn(e celast.Expr, env *joinEnv) []string {
	if env == nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(celast.Expr)
	walk = func(n celast.Expr) {
		if n == nil {
			return
		}
		switch n.Kind() {
		case celast.IdentKind:
			if _, ok := env.joins[n.AsIdent()]; ok {
				seen[n.AsIdent()] = true
			}
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

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// checkNoChains refuses a filter that traverses twice.
//
// Not a pushdown decision — a hard error, because the alternative is a false
// empty. A chain evaluates in Go against a far row that is a map of columns,
// where `a.project` is a missing key; CEL calls that an error, and a row whose
// evaluation fails does not match, so every row silently fails to match and
// the listing comes back empty rather than looking wrong.
//
// One hop is what this spike does. Saying so is the difference between a
// feature that is missing and an answer that is wrong.
func (e *joinEnv) checkNoChains(root celast.Expr, iter, relation string) error {
	if e == nil {
		return nil
	}
	far := map[string]store.ColumnType{}
	if relation != "" {
		far = columnsByName(e.columns[relation])
	}

	var found error
	var walk func(celast.Expr)
	walk = func(n celast.Expr) {
		if n == nil || found != nil {
			return
		}
		if n.Kind() == celast.SelectKind {
			sel := n.AsSelect()
			operand := sel.Operand()

			// `parent.parent.title`: a select on a select is a chain by
			// construction.
			if operand != nil && operand.Kind() == celast.SelectKind {
				found = fmt.Errorf(
					"%s reaches through two relations, which a filter cannot follow yet",
					fieldPath(n))
				return
			}
			if operand != nil && operand.Kind() == celast.IdentKind {
				name := operand.AsIdent()
				if join, ok := e.joins[name]; ok && join.Kind == store.ToOne {
					if _, isColumn := columnsByName(e.columns[name])[sel.FieldName()]; !isColumn {
						found = fmt.Errorf(
							"%s.%s: %q is not a column of %s, and a filter cannot follow "+
								"a relation of a related record yet",
							name, sel.FieldName(), sel.FieldName(), join.Table)
						return
					}
				}
				// Inside a traversal, a field of the far row that is not one
				// of its columns is the second hop.
				if iter != "" && name == iter {
					if _, isColumn := far[sel.FieldName()]; !isColumn {
						found = fmt.Errorf(
							"%s.%s: %q is not a column of %s, and a filter cannot follow "+
								"a relation of a related record yet",
							iter, sel.FieldName(), sel.FieldName(), e.joins[relation].Table)
						return
					}
				}
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
			// `c.actions.exists(...)`: a traversal whose range is a field of
			// something else is the second hop, wearing a quantifier.
			if r := c.IterRange(); r != nil && r.Kind() == celast.SelectKind {
				found = fmt.Errorf(
					"%s: a filter cannot follow a relation of a related record yet",
					fieldPath(r))
				return
			}
			walk(c.IterRange())
			walk(c.LoopCondition())
			walk(c.LoopStep())
			walk(c.Result())
		}
	}
	walk(root)
	return found
}

// fieldPath renders a select chain for an error message.
func fieldPath(e celast.Expr) string {
	if e == nil || e.Kind() != celast.SelectKind {
		return ""
	}
	sel := e.AsSelect()
	if prefix := fieldPath(sel.Operand()); prefix != "" {
		return prefix + "." + sel.FieldName()
	}
	if operand := sel.Operand(); operand != nil && operand.Kind() == celast.IdentKind {
		return operand.AsIdent() + "." + sel.FieldName()
	}
	return sel.FieldName()
}

// subquery renders a recognised traversal as SQL.
//
// The far table is aliased as the iteration variable, so the inner predicate
// converts to SQL that already qualifies its columns correctly. A junction is
// joined rather than nested: one more table in the FROM, not one more EXISTS.
func (e *joinEnv) subquery(x exists, inner string, args []any) (string, []any) {
	join := e.joins[x.relation]
	alias := x.iter

	var from strings.Builder
	fmt.Fprintf(&from, "%s %s", join.Table, alias)
	where := []string{}
	if join.Via != nil {
		fmt.Fprintf(&from, " JOIN %s j ON j.%s = %s.%s",
			join.Via.Table, join.Via.Far, alias, join.Far)
		where = append(where, fmt.Sprintf("j.%s = %s.%s", join.Via.Near, e.base, join.Near))
	} else {
		where = append(where, fmt.Sprintf("%s.%s = %s.%s", alias, join.Far, e.base, join.Near))
	}
	if inner != "" {
		where = append(where, inner)
	}

	keyword := "EXISTS"
	if x.negated {
		keyword = "NOT EXISTS"
	}
	return fmt.Sprintf("%s (SELECT 1 FROM %s WHERE %s)",
		keyword, from.String(), strings.Join(where, " AND ")), args
}

// toOne renders a comparison that reaches through a to-one relation.
//
// Also an EXISTS, so that a missing far row — a project with no parent —
// excludes rather than comparing against NULL, which is what CEL does when it
// meets a null in the middle of a field access.
func (e *joinEnv) toOne(relation, inner string, args []any) (string, []any) {
	join := e.joins[relation]
	return fmt.Sprintf("EXISTS (SELECT 1 FROM %s %s WHERE %s.%s = %s.%s AND %s)",
		join.Table, relation, relation, join.Far, e.base, join.Near, inner), args
}
