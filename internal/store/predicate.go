package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

	// Action is the action being asked about, for a predicate that needs
	// something the action carries rather than something its subject does.
	// Only a per-owner review step does today: which group it waits for is on
	// the action, because a pull request needing three reviews in order has
	// three different answers at once.
	Action *Action
	// Members are the logins belonging to that group, as last read from
	// GitHub. Empty where the group has never been read, and empty is not
	// approval.
	Members []string

	// Issue is the tracker issue the action waits on, or nil where it waits
	// on none. The one subject that is not ours: an issue in somebody else's
	// repository, which nothing here can do anything about except notice.
	Issue *TrackerIssue
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

	// PredicateApprovedBy asks about one named group rather than about the
	// pull request as a whole, which is why it is not PredicateApproved with
	// an argument: the two answer different questions and a chain can hold
	// several of this one at once.
	PredicateApprovedBy = "pr_approved_by"

	// PredicateRefExists is not prefixed pr_: it asks about a repository and
	// a pattern, and nothing about a pull request.
	PredicateRefExists = "ref_exists"

	// PredicateIssueClosed asks about a tracker issue, which is the first
	// subject that is somebody else's work rather than ours.
	PredicateIssueClosed = "issue_closed"
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

	// One named group has approved.
	//
	// Not reviewDecision, which is a single verdict for the whole pull
	// request and is what makes a three-stage review inexpressible: it says
	// APPROVED once, at the end, which is the wrong answer twice for a change
	// that needs its own team, then the owners of what it touches, then
	// whoever guards the protected parts.
	//
	// Membership is the whole of the work. An approval arrives as a login and
	// a step waits for a team, so the question is whether any approver stands
	// for the group — which is what the cached membership answers, and why a
	// group nothing has read answers false. That is the same rule as an
	// unsynced column: a wait that stays open because nothing was read is
	// visible; one that closed for that reason would not be.
	//
	// GitHub's own dismissal rules do the rest. latestOpinionatedReviews
	// carries what still counts against the current head where branch
	// protection dismisses stale approvals, and carries a standing approval
	// where it does not — which is the repository's policy on whether an
	// approval survives a push, and not ours to second-guess.
	PredicateApprovedBy: func(f Facts) bool {
		if f.PR == nil || f.Action == nil {
			return false
		}
		target := f.Action.WaitingFor
		if !target.Valid || target.String == "" {
			return false
		}
		approvals := decodeJSONStrings(f.PR.Approvals)
		if len(approvals) == 0 {
			return false
		}

		// The group may be a person — a tier that resolves to one individual
		// is a real case, and their own approval is the answer.
		if approvedBy(approvals, target.String) {
			return true
		}
		for _, member := range f.Members {
			if approvedBy(approvals, member) {
				return true
			}
		}
		return false
	},

	// The tracker says the issue closed.
	//
	// closed_at rather than the status, for the reason the week-in-review
	// listings read it: status is the tracker's own word and unconstrained on
	// purpose, so "Done", "Closed" and "Resolved" are three trackers' names
	// for one idea and deciding which of them counts is not roz's to do. A
	// time is unambiguous.
	//
	// What that costs is a tracker nothing reads. GitHub supplies the time on
	// every sync; Jira has it only where `roz issue observe --closed-at` was
	// given one, so a wait on a Jira issue closed and never recorded stays
	// open. That is roz not having been told, and it fails in the direction
	// every predicate fails in: a wait held open is visible, and one closed on
	// an absence would not be.
	PredicateIssueClosed: func(f Facts) bool {
		if f.Issue == nil {
			return false
		}
		return f.Issue.ClosedAt.Valid && f.Issue.ClosedAt.String != ""
	},

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

// approvedBy reports whether one of the approving logins is this owner.
//
// Compared the way CODEOWNERS compares: case-insensitively, and without the
// leading @, because the same person is "alice" in an approval, "@alice" in
// the file and "@Alice" in whatever somebody typed. Two spellings of one login
// is a comparison that fails silently, which here means a wait that never
// closes for a reason nothing reports.
func approvedBy(approvals []string, owner string) bool {
	want := normalizeLogin(owner)
	if want == "" {
		return false
	}
	for _, login := range approvals {
		if normalizeLogin(login) == want {
			return true
		}
	}
	return false
}

func normalizeLogin(raw string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
}

// decodeJSONStrings reads one of the observed JSON list columns, treating
// anything unreadable as empty. A malformed column is not a reason to report
// work finished.
func decodeJSONStrings(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
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
