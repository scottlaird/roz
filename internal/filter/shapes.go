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

// pushableShape reports whether an expression is a shape known to mean the
// same thing in SQLite and in CEL.
func pushableShape(e celast.Expr, columns map[string]store.ColumnType) bool {
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
		c, ok := columnOf(args[0], columns)
		return ok && c.Kind == "boolean" && !c.Nullable

	case operators.Equals:
		return comparesToLiteral(args, columns, equalsRule)

	case operators.NotEquals:
		return comparesToLiteral(args, columns, notEqualsRule)

	case operators.Less, operators.LessEquals, operators.Greater, operators.GreaterEquals:
		return comparesToLiteral(args, columns, orderedRule)

	// contains becomes INSTR, which is case-sensitive, as CEL's contains is.
	//
	// startsWith and endsWith are deliberately absent: they become LIKE, and
	// SQLite's LIKE is case-insensitive for ASCII, so `title.startsWith("Fix")`
	// would match "fix the thing" in the query and not in Go. matches is
	// absent because the dialect refuses it outright.
	case overloadContains:
		if !call.IsMemberFunction() || len(args) != 1 {
			return false
		}
		c, ok := columnOf(call.Target(), columns)
		return ok && c.Kind == "text" && !c.JSON && literalKind(args[0]) == "text"

	default:
		return false
	}
}

// overloadContains is the member function name as it appears in the AST.
const overloadContains = "contains"

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

// comparesToLiteral checks a two-argument comparison between one of the
// record's columns and a literal, in either order.
func comparesToLiteral(args []celast.Expr, columns map[string]store.ColumnType, rule comparisonRule) bool {
	if len(args) != 2 {
		return false
	}
	for _, order := range [][2]celast.Expr{{args[0], args[1]}, {args[1], args[0]}} {
		c, ok := columnOf(order[0], columns)
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

// columnOf resolves an expression to one of the record's columns.
func columnOf(e celast.Expr, columns map[string]store.ColumnType) (store.ColumnType, bool) {
	if e == nil || e.Kind() != celast.IdentKind {
		return store.ColumnType{}, false
	}
	c, ok := columns[e.AsIdent()]
	return c, ok
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
