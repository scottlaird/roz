package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Verifiable is a record carrying a last_verified_at: something that can go
// stale as opposed to merely getting old.
//
// Projects and actions qualify. A pull request does not — sync reads it every
// minute, so "when did anyone last check" is never the question about one.
type Verifiable interface {
	Record
	// verified returns a copy stamped at, for Tx.Update to diff against the
	// original. A copy rather than a mutation, so the caller keeps a before
	// image the way every other write here does.
	verified(at string) Record
}

func (p *Project) verified(at string) Record {
	clone := p.Clone()
	clone.LastVerifiedAt = sql.NullString{String: at, Valid: true}
	return clone
}

func (a *Action) verified(at string) Record {
	clone := a.Clone()
	clone.LastVerifiedAt = sql.NullString{String: at, Valid: true}
	return clone
}

// Verify records that a record was checked against reality, changing nothing
// else.
//
// The transaction must have been opened as ActorVerify: last_verified_at is
// observed, so any other actor is refused by the ordinary rule rather than by
// anything special here.
//
// Re-verifying with the same timestamp writes nothing, because the diff
// decides. That is only reachable by passing --at deliberately, and doing
// nothing is the right answer to being told the same thing twice.
func (t *Tx) Verify(ctx context.Context, r Record, at string) ([]Change, error) {
	v, ok := r.(Verifiable)
	if !ok {
		return nil, fmt.Errorf(
			"%s cannot be verified: only projects and actions record when they were last checked",
			r.subjectID())
	}
	return t.Update(ctx, r, v.verified(at))
}
