package store

import (
	"fmt"
	"sort"
)

// Facts is everything a predicate may read: the observed rows belonging to
// the action being asked about.
//
// Gathered before the predicate runs, so a predicate stays a pure function of
// stored state with no database of its own to consult. Every field is
// optional, because what an action has depends on what it is waiting for.
type Facts struct {
	// PR is the action's subject pull request, or nil where it has none.
	PR *PR
	// Wait is what the action is waiting for, when it waits on a ref.
	Wait *RefWait
	// Refs are the observed refs the wait could be satisfied by: one
	// repository, one kind. Empty where nothing has been observed yet, which
	// is not the same as the ref not existing.
	Refs []*GitRef
}

// A Predicate decides whether an action's work is finished, from the observed
// state of what it is about.
//
// It answers only "is this done", never "should this happen". Sync does not
// guess: a predicate either matches or it does not, and closing follows.
//
// Absence is not completion. Where GitHub has told us nothing — an unsynced
// pull request, a merge state it has not computed, a repository whose refs
// have never been read — a predicate returns false. Reporting done because a
// column is empty is the one failure mode that would matter.
type Predicate func(Facts) bool

// onPR adapts a predicate that reads only the subject pull request.
//
// Most of them do, and the nil check belongs in one place rather than at the
// top of each: an action with no subject has nothing to ask, and the answer is
// false for the same reason an unsynced column gives false.
func onPR(ask func(*PR) bool) Predicate {
	return func(f Facts) bool {
		if f.PR == nil {
			return false
		}
		return ask(f.PR)
	}
}

// Predicate keys. These are the names stored in actionverb.predicate_key,
// which is an informal foreign key into the registry below.
const (
	PredicateNotDraft     = "pr_not_draft"
	PredicateAnnounced    = "pr_announced"
	PredicateApproved     = "pr_approved"
	PredicateThreadsClear = "pr_threads_clear"
	PredicateMergeable    = "pr_mergeable"
	PredicateMerged       = "pr_merged"

	// PredicateRefExists is not prefixed pr_: it asks about a repository and
	// a pattern, and nothing about a pull request.
	PredicateRefExists = "ref_exists"
)

// mergeStates a rebase is meant to clear.
const (
	MergeStateBehind = "BEHIND"
	MergeStateDirty  = "DIRTY"
)

// reviewApproved is GitHub's vocabulary, not ours.
const reviewApproved = "APPROVED"

// predicates is the registry: a name in the database, a function here.
//
// The vocabulary table stores the name and never the expression. Encoding
// predicates as data would turn that table into a programming language, and
// the point of the split is that adding a human-closed verb is a row while
// adding an automated one is deliberately a row plus a function.
var predicates = map[string]Predicate{
	// A draft is not ready; anything else is. Unknown draftness is not.
	PredicateNotDraft: onPR(func(pr *PR) bool {
		return pr.IsDraft.Valid && !pr.IsDraft.Bool
	}),

	// The Slack announcement, which GitHub cannot supply. Recorded either by
	// a Slack sync or by hand with `roz pr announce`.
	PredicateAnnounced: onPR(func(pr *PR) bool {
		return pr.AnnouncedAt.Valid && pr.AnnouncedAt.String != ""
	}),

	// One approval is not APPROVED when several teams are on the request;
	// reviewDecision is GitHub's answer to that question, so it is the one
	// worth asking rather than counting approvals ourselves.
	PredicateApproved: onPR(func(pr *PR) bool {
		return pr.ReviewDecision.Valid && pr.ReviewDecision.String == reviewApproved
	}),

	// No unresolved threads against the current head. NULL means never
	// synced, which is not the same as none.
	PredicateThreadsClear: onPR(func(pr *PR) bool {
		return pr.UnresolvedThreads.Valid && pr.UnresolvedThreads.Int64 == 0
	}),

	// Nothing left for a rebase to fix. BEHIND is out of date and DIRTY is
	// conflicted; every other state is something else's problem.
	PredicateMergeable: onPR(func(pr *PR) bool {
		if !pr.MergeStateStatus.Valid {
			return false
		}
		switch pr.MergeStateStatus.String {
		case MergeStateBehind, MergeStateDirty, "":
			return false
		default:
			return true
		}
	}),

	PredicateMerged: onPR(func(pr *PR) bool {
		return pr.State.Valid && pr.State.String == PRStateMerged
	}),

	// A branch or tag matching the wait exists.
	//
	// The first predicate that reads something other than a pull request, and
	// the reason Facts is a struct. No refs is false rather than true: a
	// repository nothing has polled looks identical to one where the release
	// has not been cut, and only one of those means "carry on".
	PredicateRefExists: func(f Facts) bool {
		if f.Wait == nil {
			return false
		}
		for _, ref := range f.Refs {
			if f.Wait.Matches(ref) {
				return true
			}
		}
		return false
	},
}

// LookupPredicate returns the function a key names.
func LookupPredicate(key string) (Predicate, bool) {
	p, ok := predicates[key]
	return p, ok
}

// PredicateKeys lists every registered key, sorted.
func PredicateKeys() []string {
	keys := make([]string, 0, len(predicates))
	for key := range predicates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ErrUnregisteredPredicate reports a verb naming a predicate the code does
// not have.
type ErrUnregisteredPredicate struct {
	Verb string
	Key  string
}

func (e *ErrUnregisteredPredicate) Error() string {
	return fmt.Sprintf("verb %q closes on predicate %q, which this build does not have; known predicates are %v",
		e.Verb, e.Key, PredicateKeys())
}
