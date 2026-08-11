package store

import (
	"fmt"
	"sort"
)

// A Predicate decides whether an action's work is finished, from the observed
// state of the pull request it is about.
//
// It answers only "is this done", never "should this happen". Sync does not
// guess: a predicate either matches or it does not, and closing follows.
//
// Absence is not completion. Where GitHub has told us nothing — an unsynced
// pull request, a merge state it has not computed — a predicate returns
// false. Reporting done because a column is empty is the one failure mode
// that would matter.
type Predicate func(*PR) bool

// Predicate keys. These are the names stored in actionverb.predicate_key,
// which is an informal foreign key into the registry below.
const (
	PredicateNotDraft     = "pr_not_draft"
	PredicateAnnounced    = "pr_announced"
	PredicateApproved     = "pr_approved"
	PredicateThreadsClear = "pr_threads_clear"
	PredicateMergeable    = "pr_mergeable"
	PredicateMerged       = "pr_merged"
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
	PredicateNotDraft: func(pr *PR) bool {
		return pr.IsDraft.Valid && !pr.IsDraft.Bool
	},

	// The Slack announcement, which GitHub cannot supply. Recorded either by
	// a Slack sync or by hand with `roz pr announce`.
	PredicateAnnounced: func(pr *PR) bool {
		return pr.AnnouncedAt.Valid && pr.AnnouncedAt.String != ""
	},

	// One approval is not APPROVED when several teams are on the request;
	// reviewDecision is GitHub's answer to that question, so it is the one
	// worth asking rather than counting approvals ourselves.
	PredicateApproved: func(pr *PR) bool {
		return pr.ReviewDecision.Valid && pr.ReviewDecision.String == reviewApproved
	},

	// No unresolved threads against the current head. NULL means never
	// synced, which is not the same as none.
	PredicateThreadsClear: func(pr *PR) bool {
		return pr.UnresolvedThreads.Valid && pr.UnresolvedThreads.Int64 == 0
	},

	// Nothing left for a rebase to fix. BEHIND is out of date and DIRTY is
	// conflicted; every other state is something else's problem.
	PredicateMergeable: func(pr *PR) bool {
		if !pr.MergeStateStatus.Valid {
			return false
		}
		switch pr.MergeStateStatus.String {
		case MergeStateBehind, MergeStateDirty, "":
			return false
		default:
			return true
		}
	},

	PredicateMerged: func(pr *PR) bool {
		return pr.State.Valid && pr.State.String == PRStateMerged
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
