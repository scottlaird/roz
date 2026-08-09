package store

import (
	"context"
	"fmt"
	"strconv"
)

// Ident is an allocated identifier, split into the three columns every
// sequence-numbered entity stores: the id that leaves the database, and the
// prefix and number it is built from.
type Ident struct {
	ID   string
	Kind string
	N    int64
}

// allocate is a single autocommitted statement, which is what makes the
// number stick. RETURNING gives back the value from before the increment,
// because next_n means the next number to hand out.
const allocate = `
UPDATE sequence SET next_n = next_n + 1 WHERE entity = ? RETURNING next_n - 1, kind`

// Allocate reserves the next identifier for an entity.
//
// It deliberately does not take a Tx. Allocation commits on its own, before
// and separately from the insert that uses it, so that a failed insert still
// consumes the number. Gaps are fine; reuse is not, because an identifier may
// already have been written into a ticket or said out loud.
//
// For the same reason the sequence table is the only authority. Deriving the
// next number from max(n) would silently reissue an identifier as soon as a
// row was deleted or tombstoned.
func (s *Store) Allocate(ctx context.Context, entity Entity) (Ident, error) {
	var n int64
	var prefix string
	if err := s.db.QueryRowContext(ctx, allocate, string(entity)).Scan(&n, &prefix); err != nil {
		return Ident{}, fmt.Errorf("allocating an identifier for %s: %w", entity, err)
	}
	return Ident{ID: prefix + strconv.FormatInt(n, 10), Kind: prefix, N: n}, nil
}
