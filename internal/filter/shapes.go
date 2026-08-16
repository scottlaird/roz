package filter

import (
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/scottlaird/roz/internal/store"
)

// The rule: a term is pushed down only if it is one of the shapes below, and
// only if the probe then agrees that it means the same thing.
//
// Default deny, because the other way round was wrong twice in two hours. A
// probe can only fail to find a difference; it can never show there is none,
// and both misses came from a value nobody thought to try. `!merged_at`
// survived because every sample was NULL; `startsWith` would survive still,
// because SQLite's LIKE is case-insensitive for ASCII and every sample string
// here is lower case. An allow-list fails the other way: an expression nobody
// has thought about runs in Go, which is slower and right.
//
// The probe stays as a second gate rather than being replaced. The shapes say
// what roz believes cel2sql emits; the probe checks that it still does, so a
// change upstream costs a pushdown rather than an answer.

// scope resolves the names an expression may use to columns.
//
// Two of them, because a join predicate lives in a different namespace from a
// top-level one: `a.verb` is the far row and `project.priority` is the row
// being filtered, while a *bare* name inside a join is the trap — CEL binds it
// to the outer row and SQLite binds it to the inner table, so the same
// expression means two things. Inside a join, bare names are refused.
type scope struct {
	// base are the columns of the row being filtered.
	base map[string]store.ColumnType
	// baseName is the table, which is how a join predicate names the outer
	// row. Empty at the top level, where bare names are unambiguous.
	baseName string
	// iter is the iteration variable, and far are the columns it reaches.
	iter string
	far  map[string]store.ColumnType
}

func topLevel(columns map[string]store.ColumnType) scope {
	return scope{base: columns}
}

// resolve turns an expression into the column it names, if it names one.
func (s scope) resolve(e celast.Expr) (store.ColumnType, bool) {
	if e == nil {
		return store.ColumnType{}, false
	}
	switch e.Kind() {
	case celast.IdentKind:
		if s.baseName != "" {
			// Inside a join: see the note on scope.
			return store.ColumnType{}, false
		}
		c, ok := s.base[e.AsIdent()]
		return c, ok
	case celast.SelectKind:
		sel := e.AsSelect()
		operand := sel.Operand()
		if operand == nil || operand.Kind() != celast.IdentKind {
			return store.ColumnType{}, false
		}
		switch operand.AsIdent() {
		case s.iter:
			c, ok := s.far[sel.FieldName()]
			return c, ok
		case s.baseName:
			c, ok := s.base[sel.FieldName()]
			return c, ok
		}
	}
	return store.ColumnType{}, false
}

// pushableShape reports whether an expression is a shape known to mean the
// same thing in SQLite and in CEL.
func pushableShape(e celast.Expr, columns scope) bool {
	if e == nil || e.Kind() != celast.CallKind {
		return false
	}
	call := e.AsCall()
	args := call.Args()

	switch call.FunctionName() {
	// Both sides have to hold. An and or an or of agreeing terms agrees:
	// where SQL has NULL, CEL has false, and NULL AND TRUE, NULL OR FALSE and
	// NULL alone all exclude the row exactly as false && true, false || false
	// and false do.
	case operators.LogicalAnd, operators.LogicalOr:
		return len(args) == 2 &&
			pushableShape(args[0], columns) && pushableShape(args[1], columns)

	// A negation is only safe over a column that is a boolean and cannot be
	// absent. NOT NULL is NULL in SQL, which excludes, where CEL's !false is
	// true, which includes — and over a non-boolean SQLite coerces rather than
	// refusing, which is how `!merged_at` came to mean "reads as zero".
	case operators.LogicalNot:
		if len(args) != 1 {
			return false
		}
		c, ok := columns.resolve(args[0])
		return ok && c.Kind == "boolean" && !c.Nullable

	case operators.Equals:
		return comparesToLiteral(args, columns, equalsRule) ||
			comparesColumns(args, columns, false)

	case operators.NotEquals:
		return comparesToLiteral(args, columns, notEqualsRule) ||
			comparesColumns(args, columns, true)

	// `state in ["OPEN", "MERGED"]` is how anybody would write the flagship
	// filter, and it converts to a json_each over a json_array. NULL IN (…) is
	// NULL in SQL and false in CEL, which excludes either way — the same
	// agreement equality has, for the same reason.
	case operators.In:
		return isInSet(args, columns)

	case operators.Less, operators.LessEquals, operators.Greater, operators.GreaterEquals:
		return comparesToLiteral(args, columns, orderedRule) ||
			comparesColumns(args, columns, false)

	// contains becomes INSTR, which is case-sensitive as CEL's contains is.
	//
	// startsWith and endsWith become LIKE, which was case-insensitive for
	// ASCII and therefore refused: `title.startsWith("Fix")` matched "fix the
	// thing" in the query and not in Go. The store now opens its connections
	// with case_sensitive_like, so the two agree — and the literal's own `%`
	// and `_` are escaped by the converter, with an ESCAPE clause to match.
	//
	// matches stays absent: the dialect refuses regexes outright.
	case overloadContains, overloadStartsWith, overloadEndsWith:
		if !call.IsMemberFunction() || len(args) != 1 {
			return false
		}
		c, ok := columns.resolve(call.Target())
		return ok && c.Kind == "text" && !c.JSON && literalKind(args[0]) == "text"

	default:
		return false
	}
}

// The member function names as they appear in the AST.
const (
	overloadContains   = "contains"
	overloadStartsWith = "startsWith"
	overloadEndsWith   = "endsWith"
)

// comparisonRule decides one comparison, given the column and the literal it
// is against. A nil literal kind means the CEL literal `null`.
type comparisonRule func(c store.ColumnType, literal string) bool

// equalsRule: `col == null` is IS NULL, which is true rather than NULL, and
// CEL agrees. `col == 'x'` against a NULL column is NULL in SQL and false in
// CEL — the same exclusion by two routes. Both hold, provided the literal is
// the column's own type: SQLite compares across types by affinity, where CEL
// calls it an error, so `title > 5` is true in one and no answer in the other.
func equalsRule(c store.ColumnType, literal string) bool {
	return literal == "null" || literal == c.Kind
}

// notEqualsRule: `col != null` is IS NOT NULL and agrees. `col != 'x'` does
// not, on a column that may be absent — SQL drops the NULL row, CEL keeps it.
// On a column that cannot be absent there is no such row.
func notEqualsRule(c store.ColumnType, literal string) bool {
	if literal == "null" {
		return true
	}
	return literal == c.Kind && !c.Nullable
}

// orderedRule: a NULL operand makes the comparison NULL in SQL and an error in
// CEL, and both exclude the row. Same-type only, for affinity's sake.
func orderedRule(c store.ColumnType, literal string) bool {
	return literal == c.Kind
}

// isInSet checks `col in [literal, …]`.
//
// Every element has to be the column's own kind. A mixed list would compare
// across types, which SQLite does by affinity and CEL calls an error — the
// same trap a mistyped literal is, one container along.
func isInSet(args []celast.Expr, columns scope) bool {
	if len(args) != 2 {
		return false
	}
	c, ok := columns.resolve(args[0])
	if !ok || c.JSON {
		return false
	}
	set := args[1]
	if set == nil || set.Kind() != celast.ListKind {
		return false
	}
	elements := set.AsList().Elements()
	if len(elements) == 0 {
		// An empty set matches nothing, which both engines agree on — but it
		// is more likely a mistake than a question, and refusing it costs a
		// pushdown of a filter nobody wanted.
		return false
	}
	for _, element := range elements {
		if literalKind(element) != c.Kind {
			return false
		}
	}
	return true
}

// comparesColumns checks a comparison between two columns, which is what a
// join is usually for: "a child that outranks its parent" compares one row's
// priority to another's, and neither side is a literal.
//
// The rules are the literal ones applied to both sides. A NULL operand makes
// the comparison NULL in SQL and an error in CEL, and both exclude — except
// for `!=`, where CEL says true and SQL excludes, so that one needs both
// columns to be non-nullable.
//
// Same kind on both sides, for affinity's reason: SQLite will happily compare
// a text column to an integer one and CEL will not.
func comparesColumns(args []celast.Expr, columns scope, notEqual bool) bool {
	if len(args) != 2 {
		return false
	}
	left, leftOK := columns.resolve(args[0])
	right, rightOK := columns.resolve(args[1])
	if !leftOK || !rightOK {
		return false
	}
	if left.JSON || right.JSON || left.Kind != right.Kind {
		return false
	}
	if notEqual && (left.Nullable || right.Nullable) {
		return false
	}
	return true
}

// comparesToLiteral checks a two-argument comparison between one of the
// record's columns and a literal, in either order.
func comparesToLiteral(args []celast.Expr, columns scope, rule comparisonRule) bool {
	if len(args) != 2 {
		return false
	}
	for _, order := range [][2]celast.Expr{{args[0], args[1]}, {args[1], args[0]}} {
		c, ok := columns.resolve(order[0])
		if !ok {
			continue
		}
		if kind := literalKind(order[1]); kind != "" {
			// A JSON column compared as a scalar is not what it looks like:
			// the value is the encoded array, and cel2sql renders it as a
			// json_each subquery that SQLite rejects.
			return !c.JSON && rule(c, kind)
		}
	}
	return false
}

// literalKind names the storage kind a literal compares against, or "" when
// the expression is not a literal at all. The CEL literal `null` is its own
// answer, since it is the one that becomes IS NULL rather than a comparison.
func literalKind(e celast.Expr) string {
	if e == nil || e.Kind() != celast.LiteralKind {
		return ""
	}
	switch value := e.AsLiteral(); value.Type() {
	case types.NullType:
		return "null"
	case types.StringType:
		return "text"
	case types.IntType, types.UintType:
		return "integer"
	case types.BoolType:
		return "boolean"
	case types.DoubleType:
		return "real"
	default:
		return kindOfUnknownLiteral(value)
	}
}

// kindOfUnknownLiteral is the door left shut: a literal type nothing here
// recognises is not a shape to push down.
func kindOfUnknownLiteral(ref.Val) string { return "" }

// columnsByName indexes the record's columns for the shape check.
func columnsByName(columns []store.ColumnType) map[string]store.ColumnType {
	byName := make(map[string]store.ColumnType, len(columns))
	for _, c := range columns {
		byName[c.Name] = c
	}
	return byName
}
