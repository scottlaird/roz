package filter

import (
	"fmt"
	"sort"

	celast "github.com/google/cel-go/common/ast"

	"github.com/scottlaird/roz/internal/store"
)

// checkRelationUse refuses a relation used as if it were a column.
//
// A relation is declared to CEL as a dyn — `subject_pr.state` has to compile,
// and what a far row holds is not known until it is read — which means
// `subject_pr == "owner/repo#123"` compiles too, and then matches nothing.
// That is the worst possible answer: an empty result is indistinguishable from
// a correct empty result, and #263 was somebody concluding a pull request had
// no actions recorded when it had one, snoozed, with a reason.
//
// A misspelled column is already refused, by CEL, with a caret and a position.
// This is the gap beside it: a name that exists, in a position where it cannot
// mean anything.
//
// Reading a field, walking the relation, or asking its size are the three
// things a relation is for, so those are what is allowed. Anything else — a
// comparison, arithmetic, passing it to a function — is refused with what to
// write instead, because the useful part of this error is not that the filter
// was wrong but which form was meant.
func checkRelationUse(ast celast.Expr, joins *joinEnv) error {
	if joins == nil || ast == nil {
		return nil
	}

	// Identifiers standing where a relation is meant to stand. Collected by
	// their expression id rather than by name: `subject_pr.state ==
	// subject_pr` names it twice and only one of them is wrong.
	positioned := map[int64]bool{}
	walk(ast, func(n celast.Expr) {
		switch n.Kind() {
		case celast.SelectKind:
			mark(positioned, n.AsSelect().Operand())
		case celast.ComprehensionKind:
			mark(positioned, n.AsComprehension().IterRange())
		case celast.CallKind:
			call := n.AsCall()
			// A member call: `actions.size()`, `actions.exists(...)` before
			// the macro expands it.
			mark(positioned, call.Target())
			// And the global spelling of the same question. `size(actions)`
			// works today and answers correctly — it was checked against a
			// project with an action and one without — so refusing it would
			// break a working filter to fix a broken one. Only size: it is
			// the one function that takes a relation and means something.
			if call.FunctionName() == overloadSize && !call.IsMemberFunction() {
				for _, arg := range call.Args() {
					mark(positioned, arg)
				}
			}
		}
	})

	var wrong []string
	walk(ast, func(n celast.Expr) {
		if n.Kind() != celast.IdentKind || positioned[n.ID()] {
			return
		}
		name := n.AsIdent()
		if _, isRelation := joins.joins[name]; isRelation {
			wrong = append(wrong, name)
		}
	})
	if len(wrong) == 0 {
		return nil
	}

	sort.Strings(wrong)
	name := wrong[0]
	return fmt.Errorf(
		"%q is a relation, not a column, so comparing it matches nothing: "+
			"read a field of it (%s.<field>)%s, or ask what it holds (%s.exists(x, ...))",
		name, name, sizeHint(joins, name), name)
}

// sizeHint offers `.size()` only where it means something. A to-one relation
// has no size, and suggesting one would send somebody to a second wrong
// filter.
func sizeHint(joins *joinEnv, name string) string {
	if join, ok := joins.joins[name]; ok && join.Kind == store.ToMany {
		return fmt.Sprintf(", count it (%s.size())", name)
	}
	return ""
}

// overloadSize is CEL's size(), in both its spellings.
const overloadSize = "size"

func mark(into map[int64]bool, e celast.Expr) {
	if e != nil && e.Kind() == celast.IdentKind {
		into[e.ID()] = true
	}
}

// walk visits every node once. The traversal is the one filter.go already
// uses; it is here as a function because two passes want it.
func walk(e celast.Expr, visit func(celast.Expr)) {
	if e == nil {
		return
	}
	visit(e)
	switch e.Kind() {
	case celast.SelectKind:
		walk(e.AsSelect().Operand(), visit)
	case celast.CallKind:
		call := e.AsCall()
		walk(call.Target(), visit)
		for _, arg := range call.Args() {
			walk(arg, visit)
		}
	case celast.ListKind:
		for _, element := range e.AsList().Elements() {
			walk(element, visit)
		}
	case celast.ComprehensionKind:
		c := e.AsComprehension()
		walk(c.IterRange(), visit)
		walk(c.LoopCondition(), visit)
		walk(c.LoopStep(), visit)
		walk(c.Result(), visit)
	}
}
