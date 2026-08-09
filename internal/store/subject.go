package store

import (
	"context"
	"fmt"
)

// LoadSubject loads whatever record an identifier names, so commands that act
// on any subject — note, exception — do not need to know the entity.
//
// The identifier's prefix decides the entity, via the registry seeded at
// init. An unrecognised prefix is an error naming what is known, since the
// prefixes are the user's own choice and a typo is otherwise silent.
func (t *Tx) LoadSubject(ctx context.Context, s *Store, id string) (Record, error) {
	entity, ok := s.EntityForID(id)
	if !ok {
		return nil, fmt.Errorf("%q is not an identifier this database issues; prefixes are %s",
			id, FormatPrefixes(s.prefixes))
	}

	switch entity {
	case EntityProject:
		return t.LoadProject(ctx, id)
	default:
		// Reached when an entity gains a sequence before it gains a loader.
		return nil, fmt.Errorf("no loader for %s yet", entity)
	}
}
