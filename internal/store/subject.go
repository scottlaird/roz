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
	if entity, ok := s.EntityForID(id); ok {
		switch entity {
		case EntityProject:
			return t.LoadProject(ctx, id)
		case EntityAction:
			return t.LoadAction(ctx, id)
		default:
			// Reached when an entity gains a sequence before it gains a loader.
			return nil, fmt.Errorf("no loader for %s yet", entity)
		}
	}

	// Pull requests and repositories keep their natural keys, so they have no
	// prefix to look up. This is the heterogeneous subject_id the log is
	// designed around.
	if _, _, err := ParsePRKey(id); err == nil {
		return t.LoadPR(ctx, id)
	}
	if _, _, err := ParseRepoID(id); err == nil {
		return t.LoadGitHubRepo(ctx, id)
	}

	// A calendar window's identifier has no shape to recognise — it is
	// whatever was typed — so it is the last thing tried rather than
	// something matched.
	if w, err := t.LoadCalendarWindow(ctx, id); err == nil {
		return w, nil
	}

	return nil, fmt.Errorf(
		"%q is not an identifier this database issues (prefixes are %s), a pull request key like owner/repo%s123, or a repository like owner/repo",
		id, FormatPrefixes(s.prefixes), prSeparator)
}
