package store

import (
	"fmt"
	"strings"
)

// Effort sizes, smallest first. The order is the ordering.
const (
	EffortMinutes = "minutes"
	EffortHours   = "hours"
	EffortSession = "session"
	EffortDays    = "days"
	EffortWeeks   = "weeks"
)

// Efforts are the accepted sizes, smallest first, matching the CHECK on
// project.effort.
var Efforts = []string{EffortMinutes, EffortHours, EffortSession, EffortDays, EffortWeeks}

// RankClasses are the verb rank classes in the order a queue reads them: one
// click, then a judgement to make, then real work, then waiting on somebody
// else. Matching the CHECK on actionverb.rank_class.
//
// The class is data, so adding a verb does not mean editing the ordering.
// Which classes exist, and in what order, is not — the CHECK draws that set,
// and this states the order once.
var RankClasses = []string{RankClick, RankDecide, RankSession, RankWait}

// unblocksCTE counts, for each action, how many open actions sit downstream
// of it in the blocking graph.
//
// Transitive rather than direct, because the sketch's reason for the term is
// that "this week's most important items were the ones gating chains" — and a
// direct count gives the head of a chain of four the same 1 as something
// blocking a single leaf, which is exactly the distinction it was after.
//
// Only open followers count. Freeing something already done is worth nothing,
// and a finished chain would otherwise keep inflating its head forever.
//
// UNION rather than UNION ALL, so a cycle terminates instead of recursing
// until SQLite gives up. The schema forbids an action blocking itself and
// nothing longer than that.
//
// hidden_behind is deliberately not in here. It is a different edge — the
// judgement that there is nothing to do about a follower except clear its
// blocker — and the sketch is explicit that conflating the two regrows the
// noise the fold rule removed.
const unblocksCTE = `WITH RECURSIVE downstream(root, id) AS (
    SELECT blocker_id, blocked_id FROM action_blocks
    UNION
    SELECT d.root, e.blocked_id FROM downstream d JOIN action_blocks e ON e.blocker_id = d.id
  ),
  unblocks AS (
    SELECT d.root AS id, count(DISTINCT d.id) AS n
      FROM downstream d
      JOIN action follower ON follower.id = d.id AND follower.closed_at IS NULL
     GROUP BY d.root
  )
`

// rankJoins are the tables the ranking reads beyond action itself.
const rankJoins = ` LEFT JOIN project p ON p.id = a.project_id` +
	` LEFT JOIN actionverb v ON v.verb = a.verb` +
	` LEFT JOIN unblocks u ON u.id = a.id`

// rankOrder is the sketch's ranking, with priority where the author put it.
//
// The sketch proposes rank_class, then unblocks_count, then effort ascending,
// with rank_pin covering whatever that gets wrong. Priority is not in that
// list because the sketch predates it being used as the planning signal it
// now is, and a one-click action on a barely-wanted project outranking real
// work on the most wanted one reads as the queue ignoring what it was told.
//
// So, in order:
//
//	rank_pin      the explicit override, which exists to beat all of this
//	priority      what the author said matters, of the project this advances
//	rank_class    how big a step this is: click, decide, session, wait
//	unblocks      how much finishing it frees, most first
//	effort        of the project it advances, smallest first: prefer finishing
//	n             creation order, so the result is stable
//
// An action that advances no project takes the middle band rather than
// sorting last.
//
// Sorting it last was the same mistake as sorting it first, in the other
// direction: it lost to every action that had a priority, whatever its verb
// and whatever it would unblock, so on a queue of thirteen the bottom four
// were exactly the four with no project. `chase` actions have no project by
// nature, which is what makes both ends wrong — first would head the queue
// with every chase, and last buries the decisions that free other work. See
// #257.
//
// unstatedPriority is a band a project can actually hold, not a sentinel
// outside the range. Anything that groups by priority would draw a sentinel as
// a real band, and a band nobody can be in is worse than a wrong one.
//
// Only this term is filled. Unstated effort still sorts last, because an
// effort nobody has estimated is not evidence of being quick — where a missing
// priority is evidence of nothing at all, which is exactly the middle.
func rankOrder() string {
	return strings.Join([]string{
		"a.rank_pin IS NULL", "a.rank_pin",
		fmt.Sprintf("coalesce(p.priority, %d)", unstatedPriority),
		ordinal("v.rank_class", RankClasses),
		"coalesce(u.n, 0) DESC",
		ordinal("p.effort", Efforts),
		"a.n",
	}, ", ")
}

// unstatedPriority is where an action with no project sorts: the middle of the
// 1..9 the schema allows, so it is a band that exists.
const unstatedPriority = 5

// ordinal renders a CASE mapping known values to their position, so a column
// holding names sorts in a stated order rather than alphabetically.
//
// Anything unrecognised sorts last, which covers NULL as well: a value the
// code has never heard of is not evidence of urgency.
//
// The values are package constants matching a CHECK constraint, never caller
// input, which is why they can be written into the SQL rather than bound.
func ordinal(column string, values []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CASE %s", column)
	for i, v := range values {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", v, i)
	}
	fmt.Fprintf(&b, " ELSE %d END", len(values))
	return b.String()
}
