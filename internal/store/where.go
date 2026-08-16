package store

// SQLWhere is a WHERE fragment somebody else compiled — the SQL half of a CEL
// filter, including any correlated subquery it reaches through.
//
// Opaque here: what it means is the filter package's business, and the store's
// job is to put it in the query with its arguments in the right order.
//
// Embedded rather than repeated so that a listing gaining a filter is a line
// rather than a pattern to remember. Every listing carries one now; see
// scottlaird/roz#201.
//
// Named for what it is rather than Predicate, which in this package is already
// the rule a verb closes on.
type SQLWhere struct {
	// Where is the fragment, already parenthesised where it needs to be.
	Where string
	// WhereArgs are its parameters, in the order the fragment names them.
	WhereArgs []any
}

// clause appends the fragment to a WHERE being built, if there is one.
//
// Parenthesised, because a fragment is an expression somebody wrote and an
// `a OR b` next to an AND would otherwise bind the wrong way.
func (p SQLWhere) clause(where []string, args []any) ([]string, []any) {
	if p.Where == "" {
		return where, args
	}
	return append(where, "("+p.Where+")"), append(args, p.WhereArgs...)
}
