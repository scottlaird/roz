package ghsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/scottlaird/roz/internal/codeowners"
	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// ChangeReader reads what is needed to work out who a pull request requires:
// its files, and the CODEOWNERS on the branch it targets.
//
// Separate from Fetcher because it is a separate cost. The state query batches
// a hundred pull requests into one request; this is a paginated file list per
// pull request, so it runs for the few that moved rather than for all of them.
type ChangeReader interface {
	Change(ctx context.Context, key string) (github.Change, error)
}

// Owners is one pull request's requirement, worked out.
type Owners struct {
	PR string
	// Required is who the change needs, sorted.
	Required []string
	// Head is the commit it was worked out against.
	Head string
}

// syncOwners works out who each pull request needs, for the ones where the
// answer could have changed.
//
// Derivation rather than a read of GitHub's own answer, deliberately. Where a
// repository enforces CODEOWNERS through branch protection, GitHub computes
// required reviewers and exposes them as reviewRequests, and that is the
// better source. Where it does not, reviewRequests is empty and reviewDecision
// is null while the file still describes who ought to look — which is most of
// what makes this worth having, since those are exactly the repositories where
// nobody is told.
//
// Skipped where the head has not moved. Which files a pull request touches
// changes only when the pull request does, and this is the expensive read: a
// paginated file list, plus a CODEOWNERS fetch, per pull request.
func syncOwners(ctx context.Context, st *store.Store, reader ChangeReader, result *Result) error {
	if reader == nil {
		return nil
	}
	tracked, err := st.ListPRs(ctx, store.PRFilter{State: store.PRStateOpen})
	if err != nil {
		return err
	}

	for _, pr := range tracked {
		if !worthDeriving(pr) {
			continue
		}
		owners, err := deriveOwners(ctx, reader, pr)
		if err == nil {
			result.asked++
		}
		if err != nil {
			// One unreadable pull request must not stop the rest, and it is
			// already reported by the state poll if it has gone invisible.
			// Here it is more likely a repository whose CODEOWNERS cannot be
			// read, which is worth knowing but not worth failing over.
			if err := reportOwnersUnreadable(ctx, st, pr.ID, err); err != nil {
				return err
			}
			continue
		}
		changed, err := recordOwners(ctx, st, pr.ID, owners)
		if err != nil {
			return err
		}
		if changed {
			result.Owners = append(result.Owners, *owners)
		}
	}
	return nil
}

// worthDeriving reports whether a pull request's requirement could have
// changed since it was last worked out.
func worthDeriving(pr *store.PR) bool {
	// Nothing has observed a head, so nothing can be matched against files
	// that may not exist yet.
	if !pr.HeadSHA.Valid || pr.HeadSHA.String == "" {
		return false
	}
	// Never worked out, or worked out against a head that has since moved.
	return !pr.OwnersHead.Valid || pr.OwnersHead.String != pr.HeadSHA.String
}

// deriveOwners reads the change and matches its files against CODEOWNERS.
func deriveOwners(ctx context.Context, reader ChangeReader, pr *store.PR) (*Owners, error) {
	change, err := reader.Change(ctx, pr.ID)
	if err != nil {
		return nil, err
	}

	// A repository with no CODEOWNERS requires nobody, and that is an answer
	// rather than a failure — it is the ordinary case for most repositories.
	file, err := codeowners.ParseString(change.Codeowners)
	if err != nil {
		return nil, fmt.Errorf("reading %s CODEOWNERS: %w", change.CodeownersPath, err)
	}
	ownership := file.Of(change.Files)

	// String() rather than the bare value: the library normalises an owner to
	// "org/storage" for matching, and "@org/storage" is how it is written in
	// the file, in a review request and by anybody talking about it. An email
	// owner renders as itself, without a leading @.
	owners := make([]string, 0, len(ownership.Owners()))
	for _, owner := range ownership.Owners() {
		owners = append(owners, owner.String())
	}
	sort.Strings(owners)

	return &Owners{PR: pr.ID, Required: owners, Head: pr.HeadSHA.String}, nil
}

// recordOwners writes the requirement, reporting whether it moved.
func recordOwners(ctx context.Context, st *store.Store, key string, owners *Owners) (bool, error) {
	encoded, err := json.Marshal(owners.Required)
	if err != nil {
		return false, fmt.Errorf("encoding the owners of %s: %w", key, err)
	}

	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, key)
	if err != nil {
		return false, err
	}
	after := before.Clone()
	after.RequiredOwners = string(encoded)
	after.OwnersHead = sql.NullString{String: owners.Head, Valid: owners.Head != ""}

	changes, err := tx.Update(ctx, before, after)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	// The head moving on its own is not news: it says the answer was checked,
	// not that it changed.
	for _, change := range changes {
		if change.Column == "required_owners" {
			return true, nil
		}
	}
	return false, nil
}

// eventOwnersUnreadable is raised when who a pull request needs cannot be
// worked out.
const eventOwnersUnreadable = "owners_unreadable"

func reportOwnersUnreadable(ctx context.Context, st *store.Store, key string, cause error) error {
	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	subject, err := tx.LoadPR(ctx, key)
	if err != nil {
		return nil
	}
	// Once a day: a repository whose CODEOWNERS cannot be read stays that way,
	// and sync would otherwise say so on every poll.
	if _, err := tx.ExceptionOnce(ctx, subject, eventOwnersUnreadable,
		fmt.Sprintf("could not work out who %s needs: %v", key, cause)); err != nil {
		return err
	}
	return tx.Commit()
}
