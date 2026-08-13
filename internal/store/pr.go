package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// prSeparator joins a repo and number into an identifier, matching the
// schema's CHECK (id = repo || '#' || number).
const prSeparator = "#"

// Pull request states, GitHub's vocabulary rather than ours.
const (
	PRStateOpen   = "OPEN"
	PRStateMerged = "MERGED"
	PRStateClosed = "CLOSED"
)

// PR is a tracked pull request.
//
// It keeps its natural key instead of drawing from a sequence, which is why
// subject_id in the log is heterogeneous by design: it holds ROZ200 or
// myrepo#4174, and nothing joins on it.
//
// Almost every column is observed. Two are authored, and both are judgements
// rather than observations: Pipeline, that this one is an exception to how its
// repository normally reaches merge, and TrackedBecause, why it is tracked at
// all. The decision to track it is still the row's existence — the reason is
// a separate question, and one that only arises once you track something you
// did not write. Everything else is sync's.
type PR struct {
	ID     string `db:"id" kind:"identity"`
	Repo   string `db:"repo" kind:"identity"`
	Number int64  `db:"number" kind:"identity"`

	Title  string         `db:"title" kind:"observed"`
	Author sql.NullString `db:"author" kind:"observed"`
	URL    sql.NullString `db:"url" kind:"observed"`

	// The four that decide nearly every completion predicate.
	State            sql.NullString `db:"state" kind:"observed"`
	IsDraft          sql.NullBool   `db:"is_draft" kind:"observed"`
	ReviewDecision   sql.NullString `db:"review_decision" kind:"observed"`
	MergeStateStatus sql.NullString `db:"merge_state_status" kind:"observed"`

	// Separate from checks: a green PR check does not mean the merge queue is
	// green, and reading it that way costs queue attempts.
	InMergeQueue sql.NullBool `db:"in_merge_queue" kind:"observed"`

	// ChecksState is GitHub's rollup over every check. The checks themselves
	// are rows in pr_check: one column holding the whole map could only ever
	// diff as "the map changed", which is fifteen useless events an hour.
	ChecksState sql.NullString `db:"checks_state" kind:"observed"`

	// base_ref changing deserves an event: a stacked PR silently retargets
	// when its parent merges.
	BaseRef sql.NullString `db:"base_ref" kind:"observed"`
	HeadSHA sql.NullString `db:"head_sha" kind:"observed"`

	// One approval is not APPROVED when four teams are on the request.
	ReviewerTeams string `db:"reviewer_teams" kind:"observed" format:"json"`
	Approvals     string `db:"approvals" kind:"observed" format:"json"`

	FirstReviewRequestedAt sql.NullString `db:"first_review_requested_at" kind:"observed"`
	HumanCommentedAt       sql.NullString `db:"human_commented_at" kind:"observed"`

	// The Slack announcement is an external freeze signal — the one input
	// GitHub cannot supply.
	AnnouncedAt      sql.NullString `db:"announced_at" kind:"observed"`
	AnnouncedChannel sql.NullString `db:"announced_channel" kind:"observed"`

	// RequiredOwners is who this pull request needs, worked out from the files
	// it touches against the repository's CODEOWNERS. Empty is ambiguous on
	// its own — owned by nobody, or never worked out — and OwnersHead is what
	// tells those apart.
	//
	// Observed: which teams a change needs is a fact about the repository's
	// rules and the change's paths, not a judgement. Which of them is actually
	// being waited for is the judgement, and that is a separate authored thing
	// — see scottlaird/roz#84.
	RequiredOwners string `db:"required_owners" kind:"observed" format:"json"`
	// OwnersHead is the head RequiredOwners was worked out against, so a poll
	// can tell whether it still holds. The files a pull request touches change
	// only when the pull request does.
	OwnersHead sql.NullString `db:"owners_head" kind:"observed"`

	// MergedAt is when GitHub says it merged, not when roz saw that it had.
	//
	// The distinction is the whole point of storing it. State says where a
	// pull request is now, and the log says when roz noticed it move — which
	// for one tracked after the fact is the poll that caught up, or nothing at
	// all if it was already merged the first time it was read. Neither answers
	// "what did I merge last week". NULL means not merged.
	MergedAt sql.NullString `db:"merged_at" kind:"observed"`

	// Frozen is a generated column: either of the two above being set. The
	// database computes it, so it is never written.
	Frozen bool `db:"frozen" kind:"derived"`

	// StackedOn is derived from base_ref, but by sync rather than by the
	// database, so it is observed and not derived.
	StackedOn sql.NullString `db:"stacked_on" kind:"observed"`

	Raw          string `db:"raw" kind:"observed" format:"json"`
	TrackedSince string `db:"tracked_since" kind:"created"`

	// UnresolvedThreads counts review threads that are unresolved and not
	// outdated — the ones hanging off the current head. NULL means never
	// synced, which address_comments must not read as none.
	UnresolvedThreads sql.NullInt64 `db:"unresolved_threads" kind:"observed"`

	// LastSyncedAt is auto rather than observed: sync touches it on every
	// poll, and logging that would bury real transitions under one event per
	// pull request per cycle. It moves only when something else does, so it
	// means "when the stored state last changed", and stays NULL until the
	// first sync that finds anything.
	LastSyncedAt sql.NullString `db:"last_synced_at" kind:"auto"`

	// Pipeline names the chain this pull request instantiates, when it is not
	// the one its repository uses: a hotfix that skips review, or protected
	// code that needs more than the usual steps.
	//
	// NULL is the ordinary case and means the repository's, resolved when the
	// chain is instantiated rather than copied here at track time — so
	// changing a repository's policy reaches the pull requests that never
	// claimed an exception to it.
	Pipeline sql.NullString `db:"pipeline"`

	// TrackedBecause is why this is tracked at all: one you wrote, one
	// somebody wants your review on, or one you are only watching.
	//
	// The row's existence records the *decision* to track it. That was enough
	// while every tracked pull request was your own, and stopped being enough
	// the moment one was not: a review has different actions, and a different
	// reason to stop tracking it, from your own work.
	//
	// NULL means unstated, and there is no default. Defaulting to authored
	// would be right most of the time and would still be the tool inventing a
	// fact it cannot check.
	TrackedBecause sql.NullString `db:"tracked_because"`
}

// Why a pull request is tracked. A closed set, matching the CHECK: every
// consumer branches on it, so an unrecognised value would be a silent gap
// rather than a new case.
const (
	// TrackedAuthored is one you wrote.
	TrackedAuthored = "authored"
	// TrackedReviewing is one somebody wants your review on. This is the case
	// the column exists for.
	TrackedReviewing = "reviewing"
	// TrackedWatching is neither, but you care what happens to it.
	TrackedWatching = "watching"
)

// TrackingReasons are the accepted values, in the order they are usually met.
var TrackingReasons = []string{TrackedAuthored, TrackedReviewing, TrackedWatching}

// ValidateTrackingReason checks a reason, reporting what is accepted rather
// than only that the value was not.
func ValidateTrackingReason(reason string) error {
	return validateOneOf("reason", reason, TrackingReasons)
}

func (p *PR) table() string       { return "pr" }
func (p *PR) subjectType() string { return "pr" }
func (p *PR) subjectID() string   { return p.ID }

// PRKey builds the identifier for a repo and number.
func PRKey(repo string, number int64) string {
	return repo + prSeparator + strconv.FormatInt(number, 10)
}

// ParsePRKey splits an identifier of the form owner/repo#number.
//
// The repository half must be a full owner/name, matching GitHub's own
// shorthand: a bare name is ambiguous across owners, and pr.repo is a foreign
// key into github_repo, which is keyed that way.
func ParsePRKey(key string) (repo string, number int64, err error) {
	repo, digits, found := strings.Cut(key, prSeparator)
	if !found {
		return "", 0, fmt.Errorf("%q is not a pull request key: expected owner/repo%snumber", key, prSeparator)
	}
	if strings.Contains(digits, prSeparator) {
		return "", 0, fmt.Errorf("%q has more than one %s", key, prSeparator)
	}
	if _, _, err := ParseRepoID(repo); err != nil {
		return "", 0, fmt.Errorf("%q: %w", key, err)
	}

	number, err = strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("%q has no pull request number", key)
	}
	if number <= 0 {
		return "", 0, fmt.Errorf("%q has a pull request number of %d", key, number)
	}
	return repo, number, nil
}

// NewPR returns an unsaved PR.
//
// There is no identifier to allocate: a pull request is named by its repo and
// number, so tracking the same one twice is a primary key conflict rather
// than a second row.
//
// Nothing else is filled in but the empty JSON containers, which match the
// schema's defaults so that the record matches the row it will become. They
// are not observations, so tracking is still something a human may do.
func NewPR(repo string, number int64) *PR {
	return &PR{
		ID:             PRKey(repo, number),
		Repo:           repo,
		Number:         number,
		ReviewerTeams:  "[]",
		Approvals:      "[]",
		RequiredOwners: "[]",
		Raw:            "{}",
	}
}

// LoadPR reads a pull request by key, returning sql.ErrNoRows if it is not
// tracked.
func (t *Tx) LoadPR(ctx context.Context, id string) (*PR, error) {
	var p PR
	if err := t.Load(ctx, &p, id); err != nil {
		return nil, err
	}
	return &p, nil
}

// Clone returns a copy to mutate, leaving the original as the before image
// for Tx.Update.
func (p *PR) Clone() *PR {
	clone := *p
	return &clone
}

// PRFilter narrows ListPRs. The zero value selects everything.
type PRFilter struct {
	// Stacked keeps pull requests based on another tracked one. Every stacked
	// PR needs saying out loud, every time.
	Stacked bool
	// Frozen keeps those that have been announced or commented on, where the
	// amend-versus-new-commit rule applies.
	Frozen bool
	// State keeps one of OPEN, MERGED or CLOSED.
	State string
	// Because keeps pull requests tracked for one reason — most usefully
	// "what am I on the hook to review", which the row's existence could not
	// answer.
	Because string
	// Since keeps pull requests merged at or after a timestamp.
	//
	// It reads merged_at, so it selects merged pull requests and nothing else
	// — "since" is about when something finished, and an open pull request has
	// not. Setting it alongside State is allowed and redundant rather than
	// contradictory.
	//
	// Compared as text, which is correct because both sides are ISO-8601 UTC.
	Since string
}

// mergeOrdered reports whether this filter is asking about merges, which is
// what decides the order rows come back in.
//
// A listing of what merged wants the week in order; a listing of what is
// tracked wants it grouped by repository. Neither order is right for the other
// question, and which question is being asked is legible from the filter.
func (f PRFilter) mergeOrdered() bool {
	return f.Since != "" || f.State == PRStateMerged
}

// ListPRs returns tracked pull requests matching the filter.
//
// Ordered by repository and number, or oldest merge first when the filter is
// about merges — which is how a week reads.
func (s *Store) ListPRs(ctx context.Context, filter PRFilter) ([]*PR, error) {
	fields, err := fieldsOfStruct(&PR{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	where, args := filter.clauses()
	query := fmt.Sprintf("SELECT %s FROM pr", strings.Join(columns, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if filter.mergeOrdered() {
		// Anything merged without a time goes last rather than first, which is
		// where SQLite puts NULL on its own. A pull request merged before
		// merged_at existed is the case, and it is not news from any week.
		query += " ORDER BY merged_at IS NULL, merged_at, repo, number"
	} else {
		query += " ORDER BY repo, number"
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing pull requests: %w", err)
	}
	defer rows.Close()

	var prs []*PR
	for rows.Next() {
		var p PR
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&p)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing pull requests: %w", err)
		}
		prs = append(prs, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing pull requests: %w", err)
	}
	return prs, nil
}

func (f PRFilter) clauses() ([]string, []any) {
	var where []string
	var args []any

	if f.Stacked {
		where = append(where, "stacked_on IS NOT NULL")
	}
	if f.Frozen {
		where = append(where, "frozen = 1")
	}
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if f.Because != "" {
		where = append(where, "tracked_because = ?")
		args = append(args, f.Because)
	}
	if f.Since != "" {
		where = append(where, "merged_at >= ?")
		args = append(args, f.Since)
	}
	return where, args
}
