package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Entity is a kind of thing that draws identifiers from its own numbering
// sequence. Entity values are ours and fixed; the prefix each one renders
// with is the user's, chosen at init.
type Entity string

const (
	EntityProject Entity = "project"
	EntityAction  Entity = "action"
)

// Entities lists every entity requiring a sequence row. It must agree with
// the CHECK constraint on sequence.entity.
var Entities = []Entity{EntityProject, EntityAction}

// firstN is the first identifier number handed out, so ids start at 1.
const firstN = 1

// ValidatePrefix reports whether s can be used as an identifier prefix.
//
// Prefixes are letters only. The binding constraint is that a prefix must not
// end in a digit: ids are the prefix concatenated with a number, so a prefix
// of "S1" makes "S12" ambiguous between S1+2 and S+12. Restricting to letters
// is a simpler rule than that and easier to report. One prefix being a
// leading substring of another stays unambiguous, since the remainder must
// parse as digits, so it is allowed.
func ValidatePrefix(s string) error {
	if s == "" {
		return fmt.Errorf("prefix is empty")
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return fmt.Errorf("prefix %q contains %q; prefixes must be letters only", s, r)
		}
	}
	return nil
}

// validatePrefixes checks that every entity has a distinct, usable prefix.
func validatePrefixes(prefixes map[Entity]string) error {
	seen := make(map[string]Entity, len(prefixes))
	for _, entity := range Entities {
		prefix, ok := prefixes[entity]
		if !ok {
			return fmt.Errorf("no prefix given for %s", entity)
		}
		if err := ValidatePrefix(prefix); err != nil {
			return fmt.Errorf("%s: %w", entity, err)
		}
		if other, dup := seen[prefix]; dup {
			return fmt.Errorf("%s and %s cannot share the prefix %q", other, entity, prefix)
		}
		seen[prefix] = entity
	}
	return nil
}

// LoadPrefixes reads the identifier prefix configured for each entity.
//
// This is how every command learns the prefixes: they are stored in the
// database at init rather than configured per-invocation, so an identifier is
// always interpreted by the database that issued it.
func LoadPrefixes(db *sql.DB) (map[Entity]string, error) {
	rows, err := db.Query("SELECT entity, kind FROM sequence")
	if err != nil {
		return nil, fmt.Errorf("reading identifier prefixes: %w", err)
	}
	defer rows.Close()

	prefixes := make(map[Entity]string)
	for rows.Next() {
		var entity, prefix string
		if err := rows.Scan(&entity, &prefix); err != nil {
			return nil, fmt.Errorf("reading identifier prefixes: %w", err)
		}
		prefixes[Entity(entity)] = prefix
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading identifier prefixes: %w", err)
	}

	for _, entity := range Entities {
		if _, ok := prefixes[entity]; !ok {
			return nil, fmt.Errorf("database has no sequence row for %s", entity)
		}
	}
	return prefixes, nil
}

func seedSequences(tx *sql.Tx, prefixes map[Entity]string) error {
	for _, entity := range Entities {
		_, err := tx.Exec(
			"INSERT INTO sequence (entity, kind, next_n) VALUES (?, ?, ?)",
			string(entity), prefixes[entity], firstN)
		if err != nil {
			return fmt.Errorf("seeding %s sequence: %w", entity, err)
		}
	}
	return nil
}

// SplitIdent separates an identifier into its prefix and number.
//
// It works because prefixes are letters only and the number is all digits, so
// the boundary is unambiguous — that restriction in ValidatePrefix is what
// buys this, and the same rule is applied here.
//
// Anything not of that shape is rejected rather than split on a best guess.
// That matters because subject_id in the log is heterogeneous by design: it
// holds TD1 or myrepo#4174, and only the first is a sequence identifier.
func SplitIdent(id string) (prefix string, n int64, ok bool) {
	split := strings.LastIndexFunc(id, func(r rune) bool {
		return r < '0' || r > '9'
	}) + 1
	if split == 0 || split == len(id) {
		return "", 0, false
	}

	prefix = id[:split]
	if err := ValidatePrefix(prefix); err != nil {
		return "", 0, false
	}
	n, err := strconv.ParseInt(id[split:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return prefix, n, true
}

// EntityForID reports which entity an identifier belongs to, by looking its
// prefix up in the registry seeded at init.
//
// This is why the prefixes are stored rather than hardcoded: the mapping from
// TD to project is the user's, made once, and read back here.
func (s *Store) EntityForID(id string) (Entity, bool) {
	prefix, _, ok := SplitIdent(id)
	if !ok {
		return "", false
	}
	for entity, entityPrefix := range s.prefixes {
		if entityPrefix == prefix {
			return entity, true
		}
	}
	return "", false
}

// FormatPrefixes renders prefixes for display, ordered by entity name.
func FormatPrefixes(prefixes map[Entity]string) string {
	pairs := make([]string, 0, len(prefixes))
	for entity, prefix := range prefixes {
		pairs = append(pairs, fmt.Sprintf("%s=%s", entity, prefix))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ", ")
}
