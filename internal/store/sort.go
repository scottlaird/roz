package store

import (
	"fmt"
	"strings"
)

// Ordering a listing by its own columns, for the listings that let a caller
// choose — see scottlaird/roz#169.
//
// The rankings are a different thing and stay where they are. OrderPriority is
// a CTE, two joins and an expression over three tables; OrderStaleness is a
// computed date. Neither is a column, so neither can be asked for here, and
// both keep their own names in the flag that resolves to one of these.

// SortKey is one column to order by.
type SortKey struct {
	// Column is a column of the record being listed. It is checked against
	// the record before a Sort exists — see NewSort.
	Column string
	// Desc reverses that one key, and only that one.
	Desc bool
}

// Sort is an ordering asked for by name: primary key first, then the
// tie-breakers in the order they were given.
//
// The keys are unexported and the only way in is NewSort, which is the point.
// This ends up interpolated into an ORDER BY, because SQLite has no way to
// bind a column name as a parameter — so the guarantee that every column here
// came off a record's own struct tags has to be one the type makes, not one
// each call site remembers.
type Sort struct {
	keys []SortKey
}

// NewSort checks the columns exist on the record and returns the ordering.
//
// An unknown column is an error naming what there was, rather than a query
// that fails somewhere further down with SQLite's own words.
func NewSort(blank any, keys []SortKey) (Sort, error) {
	if len(keys) == 0 {
		return Sort{}, nil
	}
	names, err := Columns(blank)
	if err != nil {
		return Sort{}, err
	}
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}

	for _, key := range keys {
		if !known[key.Column] {
			return Sort{}, fmt.Errorf("%q is not a column to sort on: use %s",
				key.Column, strings.Join(names, ", "))
		}
	}
	return Sort{keys: append([]SortKey(nil), keys...)}, nil
}

// Empty reports whether nothing was asked for, in which case a listing keeps
// the order it has always had.
func (s Sort) Empty() bool { return len(s.keys) == 0 }

// SQL renders the ORDER BY body, qualified by prefix where the query aliases
// its table. Empty when nothing was asked for, so a caller can fall back to
// its own default.
//
// NULLs sort last whichever direction the key runs. SQLite puts them first
// ascending and last descending, which means "sort by when it merged" quietly
// leads with everything that never did — and the rows a person is looking for
// start below the fold. Absence is not a small value, and it is not a large
// one either; it is the least interesting thing in the column.
func (s Sort) SQL(prefix string) string {
	if len(s.keys) == 0 {
		return ""
	}
	terms := make([]string, 0, len(s.keys))
	for _, key := range s.keys {
		column := prefix + key.Column
		term := column + " IS NULL, " + column
		if key.Desc {
			term += " DESC"
		}
		terms = append(terms, term)
	}
	return strings.Join(terms, ", ")
}
