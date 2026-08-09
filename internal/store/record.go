package store

import "fmt"

// Record is an entity that lives in its own table and in the event log.
//
// The methods are unexported, so records are defined in this package. That is
// deliberate: a record's tags decide who may write each of its columns, and
// that rule should not be extensible from outside.
type Record interface {
	table() string
	subjectType() string
	subjectID() string
}

// Change is one column whose value differs between two versions of a record.
type Change struct {
	Column string
	Kind   FieldKind
	Old    string
	New    string
}

func (c Change) String() string {
	return fmt.Sprintf("%s: %q → %q", c.Column, c.Old, c.New)
}

// diff reports the columns that differ between before and after.
//
// derived and auto columns are skipped: the database owns the first and the
// store owns the second, so neither is a change the caller made. A differing
// identity column is an error rather than a change — identifiers are stable
// names, and rewriting one would orphan every reference to it.
//
// Comparison is on rendered text, not on Go values, so two values that store
// identically never produce a spurious event.
func diff(before, after Record) ([]Change, error) {
	if before.subjectID() != after.subjectID() {
		return nil, fmt.Errorf("cannot diff %s against %s", before.subjectID(), after.subjectID())
	}
	if fmt.Sprintf("%T", before) != fmt.Sprintf("%T", after) {
		return nil, fmt.Errorf("cannot diff %T against %T", before, after)
	}

	fields, err := fieldsOf(before)
	if err != nil {
		return nil, err
	}

	var changes []Change
	for _, f := range fields {
		if f.kind == Derived || f.kind == Auto {
			continue
		}

		oldText, err := renderValue(f.value(before))
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", before.table(), f.column, err)
		}
		newText, err := renderValue(f.value(after))
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", after.table(), f.column, err)
		}
		if oldText == newText {
			continue
		}
		if f.kind == Identity || f.kind == Created {
			return nil, fmt.Errorf(
				"%s.%s is immutable, but changed from %q to %q",
				before.table(), f.column, oldText, newText)
		}
		changes = append(changes, Change{Column: f.column, Kind: f.kind, Old: oldText, New: newText})
	}
	return changes, nil
}
