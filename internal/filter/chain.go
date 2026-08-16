package filter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"

	"github.com/spandigital/cel2sql/v3/schema"

	"github.com/scottlaird/roz/internal/store"
)

// A chain is a filter that follows more than one relation:
// `actions.exists(a, a.project.priority == 1)` — pull requests whose action
// advances a high-priority project.
//
// One hop names a thing and two hops name a property, which is why the second
// is the more useful question: "PRs for SL41" is a lookup, "PRs for anything
// urgent" is the one somebody asks every morning.
//
// # Nested EXISTS, correlated outward
//
// Each level is a subquery over the far table, aliased by the iteration
// variable, correlated to the level above it by the same key the one-hop case
// uses. So the whole thing composes: the generator that renders one traversal
// renders the next by recursion, and the only new machinery is a scope that
// knows the far entity's *relations* rather than only its columns.
//
// # All or nothing
//
// A chain that cannot be converted whole is a hard error, never a term that
// falls back to Go. That is not caution for its own sake: in Go the far row is
// a map of columns, `a.project` is a missing key, CEL calls a missing key an
// error, and a row whose evaluation errors does not match — so the listing
// comes back empty rather than wrong-looking. #200 has the transcript of that
// happening against real data. A refusal is a feature that is missing; a false
// empty is an answer that is wrong.
//
// The Go path stays refused until far rows are records that can load their own
// relations, which is the other half of #200 and is not here.

// maxHops is how many relations a filter may follow.
//
// `parent.parent.parent…` is finite but unbounded, and
// `children.exists(c, c.children.exists(…))` can be written to any depth. Each
// level is a nested subquery, so the cost is multiplicative and the person
// waiting for the answer is the person who wrote the expression. Three is
// past every question anybody has actually asked and short of the ones that
// do not come back.
const maxHops = 3

// errTooDeep is a chain longer than maxHops. Hard, like every chain refusal:
// the alternative is an answer nobody can tell is wrong.
var errTooDeep = errors.New("too many relations")

// level is one row in a chain: the entity a predicate is being written
// against, and everything a converter needs to name it.
type level struct {
	// env holds this entity's relations, with base set to this level's own
	// alias so a subquery below it correlates to the right row.
	env *joinEnv
	// alias is the SQL alias and the CEL name of this row.
	alias string
	// columns are this row's own columns.
	columns []store.ColumnType
	// visible is every alias in scope here, including the levels above.
	visible map[string][]store.ColumnType
	// depth counts relations followed to get here. Zero is the row being
	// listed.
	depth int
}

// topFor is the level the whole filter is written against.
func topFor(joins *joinEnv, columns []store.ColumnType) level {
	return level{
		env:     joins,
		alias:   joins.base,
		columns: columns,
		visible: map[string][]store.ColumnType{joins.base: columns},
	}
}

// descend builds the level a traversal lands on.
func (l level) descend(relation, alias string) (level, error) {
	join, ok := l.env.joins[relation]
	if !ok || join.Blank == nil {
		return level{}, fmt.Errorf("%s is not a relation of %s", relation, l.alias)
	}
	child, err := newJoinEnv(join.Blank())
	if err != nil {
		return level{}, err
	}
	if child == nil {
		// The far side is not one of roz's entities, so it has columns and no
		// connections. One more hop from here is what does not exist.
		child = &joinEnv{joins: map[string]store.Join{}, columns: map[string][]store.ColumnType{}}
	}
	// The far entity's relations, correlated to this alias rather than to the
	// table it came from: `a` is what the level below has to point at.
	child.base = alias

	columns := l.env.columns[relation]
	visible := make(map[string][]store.ColumnType, len(l.visible)+1)
	for name, cols := range l.visible {
		visible[name] = cols
	}
	visible[alias] = columns

	return level{
		env:     child,
		alias:   alias,
		columns: columns,
		visible: visible,
		depth:   l.depth + 1,
	}, nil
}

// predicateSQL renders one level's predicate, recursing for each traversal in
// it.
//
// Conjunct by conjunct, because a predicate mixes the two kinds: `a.verb ==
// "merge" && a.project.priority == 1` is one term the converter can take and
// one that needs a subquery of its own. Splitting on the AST rather than on
// text, which is available here and exact.
func predicateSQL(env *cel.Env, info *celast.SourceInfo, pred celast.Expr, l level) (string, []any, error) {
	if pred == nil {
		return "", nil, nil
	}
	if l.depth > maxHops {
		return "", nil, fmt.Errorf("%w: a filter follows at most %d", errTooDeep, maxHops)
	}

	var clauses []string
	var args []any
	for _, term := range andTerms(pred) {
		sql, params, err := termSQL(env, info, term, l)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, sql)
		args = append(args, params...)
	}
	if len(clauses) == 1 {
		return clauses[0], args, nil
	}
	return "(" + strings.Join(clauses, " AND ") + ")", args, nil
}

// termSQL renders one conjunct: a traversal to recurse into, or a comparison
// the converter can take.
func termSQL(env *cel.Env, info *celast.SourceInfo, term celast.Expr, l level) (string, []any, error) {
	if x, ok := existsAt(term, l, info); ok {
		return quantifiedSQL(env, info, x, l)
	}
	if relation, ok := toOneAt(term, l); ok {
		return reachedSQL(env, info, term, relation, l)
	}
	if path, ok := reusedAlias(term, l); ok {
		return "", nil, fmt.Errorf(
			"%s reaches through two relations of the same name, which a filter "+
				"cannot follow yet: each level would alias the same table", path)
	}
	return convertAt(env, info, term, l)
}

// reusedAlias finds a term that follows a relation whose name is already an
// alias in scope, so the refusal can name it rather than reporting a
// conversion failure.
func reusedAlias(e celast.Expr, l level) (string, bool) {
	var found string
	var walk func(celast.Expr)
	walk = func(n celast.Expr) {
		if n == nil || found != "" {
			return
		}
		if n.Kind() == celast.SelectKind {
			sel := n.AsSelect()
			if operand := sel.Operand(); operand != nil && operand.Kind() == celast.SelectKind {
				inner := operand.AsSelect()
				if name, ok := relationNameAt(inner.Operand(), l); ok {
					if _, isRelation := l.env.joins[name]; isRelation {
						found = fieldPath(n)
						return
					}
				}
			}
			walk(sel.Operand())
			return
		}
		switch n.Kind() {
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
	return found, found != ""
}

// quantifiedSQL renders `rel.exists(v, pred)` at this level, and whatever the
// predicate holds beneath it.
func quantifiedSQL(env *cel.Env, info *celast.SourceInfo, x exists, l level) (string, []any, error) {
	child, err := l.descend(x.relation, x.iter)
	if err != nil {
		return "", nil, err
	}
	if child.depth > maxHops {
		return "", nil, fmt.Errorf("%w: %s.%s is %d relations deep and a filter follows at most %d",
			errTooDeep, l.alias, x.relation, child.depth, maxHops)
	}

	inner := ""
	var args []any
	if x.predicate != nil {
		scoped, err := env.Extend(cel.Variable(x.iter, cel.DynType))
		if err != nil {
			return "", nil, fmt.Errorf("scoping %q: %w", x.iter, err)
		}
		if inner, args, err = predicateSQL(scoped, info, x.predicate, child); err != nil {
			return "", nil, err
		}
	}
	sql, params := l.env.subquery(x, inner, args)
	return sql, params, nil
}

// reachedSQL renders a term that reaches through a to-one relation, with
// whatever it compares rendered against the far row.
func reachedSQL(env *cel.Env, info *celast.SourceInfo, term celast.Expr, relation string, l level) (string, []any, error) {
	child, err := l.descend(relation, relation)
	if err != nil {
		return "", nil, err
	}
	if child.depth > maxHops {
		return "", nil, fmt.Errorf("%w: %s.%s is %d relations deep and a filter follows at most %d",
			errTooDeep, l.alias, relation, child.depth, maxHops)
	}

	// The far row is named by the relation, which is what the one-hop case
	// already does: `parent.priority` reads as a column of `parent`, and the
	// subquery aliases the table to match.
	//
	// Which means the term has to be rewritten as it descends: inside the
	// traversal the far row was reached as `a.project`, and in the subquery it
	// is `project`. Rewritten on the tree rather than on the unparsed text,
	// because a filter is somebody's typing and string surgery on it is how
	// the wrong column gets compared.
	rebased := rebase(term, l.alias, relation)

	scoped, err := env.Extend(cel.Variable(relation, cel.DynType))
	if err != nil {
		return "", nil, fmt.Errorf("scoping %q: %w", relation, err)
	}
	inner, args, err := predicateSQL(scoped, info, rebased, child)
	if err != nil {
		return "", nil, err
	}
	sql, params := l.env.toOne(relation, inner, args)
	return sql, params, nil
}

// rebase rewrites `<alias>.<relation>` into `<relation>`, which is what the
// far row is called once its table has been aliased in the subquery.
//
// Only that shape, and only at this level: everything else is copied through,
// so a comparison against the outer row keeps naming the outer row.
func rebase(e celast.Expr, alias, relation string) celast.Expr {
	if e == nil {
		return nil
	}
	f := celast.NewExprFactory()

	var walk func(celast.Expr) celast.Expr
	walk = func(n celast.Expr) celast.Expr {
		if n == nil {
			return nil
		}
		switch n.Kind() {
		case celast.SelectKind:
			sel := n.AsSelect()
			operand := sel.Operand()
			// The reference being rewritten: `a.project` becomes `project`.
			if operand != nil && operand.Kind() == celast.IdentKind &&
				operand.AsIdent() == alias && sel.FieldName() == relation {
				return f.NewIdent(n.ID(), relation)
			}
			return f.NewSelect(n.ID(), walk(operand), sel.FieldName())
		case celast.CallKind:
			call := n.AsCall()
			args := make([]celast.Expr, 0, len(call.Args()))
			for _, arg := range call.Args() {
				args = append(args, walk(arg))
			}
			if call.IsMemberFunction() {
				return f.NewMemberCall(n.ID(), call.FunctionName(), walk(call.Target()), args...)
			}
			return f.NewCall(n.ID(), call.FunctionName(), args...)
		case celast.ListKind:
			list := n.AsList()
			elements := make([]celast.Expr, 0, list.Size())
			for _, element := range list.Elements() {
				elements = append(elements, walk(element))
			}
			return f.NewList(n.ID(), elements, list.OptionalIndices())
		default:
			return f.CopyExpr(n)
		}
	}
	return walk(e)
}

// convertAt hands one term to cel2sql, in this level's namespace.
func convertAt(env *cel.Env, info *celast.SourceInfo, term celast.Expr, l level) (string, []any, error) {
	if !pushableShape(term, l.scope()) {
		return "", nil, errNotEquivalent
	}
	ast, err := celFor(env, term, info)
	if err != nil {
		return "", nil, err
	}
	sql, args, err := toSQL(ast, levelSchemas(l))
	if err != nil {
		return "", nil, err
	}
	return sql, args, nil
}

// andTerms splits a predicate at top-level `&&`, which is where a chain and a
// column comparison sit side by side.
func andTerms(e celast.Expr) []celast.Expr {
	if call, ok := asCall(e, operators.LogicalAnd); ok && len(call.Args()) == 2 {
		return append(andTerms(call.Args()[0]), andTerms(call.Args()[1])...)
	}
	return []celast.Expr{e}
}

// existsAt recognises a quantified traversal written against this level.
//
// At the top that is `rel.exists(v, p)`, the shape the one-hop case already
// knows. Below it the relation hangs off the level's alias — `a.actions` —
// which is a select rather than an identifier, and is the shape that used to
// be refused as "a relation of a related record".
func existsAt(e celast.Expr, l level, info *celast.SourceInfo) (exists, bool) {
	if call, ok := asCall(e, operators.LogicalNot); ok && len(call.Args()) == 1 {
		inner, found := existsAt(call.Args()[0], l, info)
		if !found {
			return exists{}, false
		}
		inner.negated = !inner.negated
		return inner, true
	}
	if call, ok := asCall(e, operators.Equals); ok {
		if rel, empty := asEmptyCheckAt(call.Args(), l); empty {
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
	name, ok := relationNameAt(call.Target(), l)
	if !ok {
		return exists{}, false
	}
	if join, ok := l.env.joins[name]; !ok || join.Kind != store.ToMany {
		return exists{}, false
	}
	iter := call.Args()[0]
	if iter.Kind() != celast.IdentKind {
		return exists{}, false
	}
	return exists{relation: name, iter: iter.AsIdent(), predicate: call.Args()[1]}, true
}

// asEmptyCheckAt is asEmptyCheck against a level rather than the base entity.
func asEmptyCheckAt(args []celast.Expr, l level) (string, bool) {
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
		name, ok := relationNameAt(target, l)
		if !ok {
			continue
		}
		if join, ok := l.env.joins[name]; ok && join.Kind == store.ToMany {
			return name, true
		}
	}
	return "", false
}

// relationNameAt reads the relation an expression names at this level: a bare
// identifier at the top, or a field of this level's alias below it.
func relationNameAt(e celast.Expr, l level) (string, bool) {
	if e == nil {
		return "", false
	}
	switch e.Kind() {
	case celast.IdentKind:
		if l.depth > 0 {
			// Below the top level a bare name would bind to the innermost
			// table in SQL and to something else in CEL, which is the
			// ambiguity the outer row is named to avoid.
			return "", false
		}
		return e.AsIdent(), true
	case celast.SelectKind:
		sel := e.AsSelect()
		operand := sel.Operand()
		if operand == nil || operand.Kind() != celast.IdentKind || operand.AsIdent() != l.alias {
			return "", false
		}
		return sel.FieldName(), true
	}
	return "", false
}

// toOneAt names the to-one relation a term reaches at this level, if it
// reaches exactly one.
//
// Exactly one, because two relations in a term needs two subqueries and a
// decision about how they compose — the same limit the one-hop case has.
func toOneAt(e celast.Expr, l level) (string, bool) {
	seen := map[string]bool{}
	var walk func(celast.Expr)
	walk = func(n celast.Expr) {
		if n == nil {
			return
		}
		switch n.Kind() {
		case celast.SelectKind:
			sel := n.AsSelect()
			if name, ok := relationNameAt(sel.Operand(), l); ok {
				if join, found := l.env.joins[name]; found && join.Kind == store.ToOne {
					seen[name] = true
				}
			}
			walk(sel.Operand())
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

	if len(seen) != 1 {
		return "", false
	}
	for name := range seen {
		// A relation whose name is already an alias in scope cannot be
		// descended into: the subquery aliases the far table by the relation's
		// name, and two levels called `parent` would correlate the inner row
		// to itself — `parent.id = parent.parent_id`, which is SQL that runs
		// and answers nonsense. `parent.parent.title` is the case, and it
		// stays refused until levels carry distinct aliases.
		if _, taken := l.visible[name]; taken {
			return "", false
		}
		return name, true
	}
	return "", false
}

// scope is the namespace a term at this level is checked in.
func (l level) scope() scope {
	s := scope{base: columnsByName(l.columns)}
	if l.depth > 0 || l.env.base != "" {
		// Inside a subquery nothing is unqualified: a bare name binds to the
		// innermost table in SQL and to the outer row in CEL, which is the
		// divergence the allow-list exists to catch.
		s.baseName = l.alias
	}
	s.iter = l.alias
	s.far = columnsByName(l.columns)
	return s
}

// levelSchemas describes every alias in scope, plus this level's relations,
// to the converter.
func levelSchemas(l level) map[string]schema.Schema {
	schemas := map[string]schema.Schema{tableName: schemaOf(l.columns)}
	for alias, columns := range l.visible {
		schemas[alias] = schemaOf(columns)
	}
	for name := range l.env.joins {
		if far, ok := l.env.columns[name]; ok {
			schemas[name] = schemaOf(far)
		}
	}
	return schemas
}

// errChainInGo is a chain that would have had to finish in Go.
//
// Hard, unlike errNotEquivalent, which means "somewhere else should run this".
// There is nowhere else: the Go pass reads far rows as maps of columns, so the
// second hop is a missing key, CEL calls that an error, and a row whose
// evaluation errors does not match. The listing would come back empty.
var errChainInGo = errors.New(
	"a filter cannot follow two relations in Go yet, and would answer empty rather than wrong")

// chained reports whether an expression follows more than one relation, which
// is what the recursive generator is for.
//
// Cheap and syntactic: a select on a select, a traversal whose range is a
// field of something, or a traversal whose predicate contains either.
func chained(e celast.Expr, l level, info *celast.SourceInfo) bool {
	found := false
	var walk func(celast.Expr, level)
	walk = func(n celast.Expr, at level) {
		if n == nil || found {
			return
		}
		if x, ok := existsAt(n, at, info); ok {
			child, err := at.descend(x.relation, x.iter)
			if err != nil {
				return
			}
			if x.predicate != nil {
				if reachesRelation(x.predicate, child, info) {
					found = true
					return
				}
				walk(x.predicate, child)
			}
			return
		}
		switch n.Kind() {
		case celast.SelectKind:
			walk(n.AsSelect().Operand(), at)
		case celast.CallKind:
			call := n.AsCall()
			walk(call.Target(), at)
			for _, arg := range call.Args() {
				walk(arg, at)
			}
		case celast.ListKind:
			for _, element := range n.AsList().Elements() {
				walk(element, at)
			}
		}
	}
	walk(e, l)
	return found
}

// reachesRelation reports whether a predicate names any relation of the level
// it is written against, which inside a traversal is the second hop.
func reachesRelation(e celast.Expr, l level, info *celast.SourceInfo) bool {
	if _, ok := existsAt(e, l, info); ok {
		return true
	}
	if _, ok := toOneAt(e, l); ok {
		return true
	}
	found := false
	var walk func(celast.Expr)
	walk = func(n celast.Expr) {
		if n == nil || found {
			return
		}
		switch n.Kind() {
		case celast.SelectKind:
			sel := n.AsSelect()
			if name, ok := relationNameAt(sel.Operand(), l); ok {
				if _, isRelation := l.env.joins[name]; isRelation {
					found = true
					return
				}
			}
			walk(sel.Operand())
		case celast.CallKind:
			call := n.AsCall()
			if _, ok := existsAt(n, l, info); ok {
				found = true
				return
			}
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
	return found
}
