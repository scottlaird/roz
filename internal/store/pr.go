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
// subject_id in the log is heterogeneous by design: it holds TD200 or
// myrepo#4174, and nothing joins on it.
//
// Almost every column is observed. Only the decision to track the pull
// request is a judgement, and that decision is the existence of the row —
// there is no column for it. So a human may create one and then never write
// to it again; everything after that is sync's.
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

	ChecksState sql.NullString `db:"checks_state" kind:"observed"`
	Checks      string         `db:"checks" kind:"observed" format:"json"`

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
		ID:            PRKey(repo, number),
		Repo:          repo,
		Number:        number,
		Checks:        "{}",
		ReviewerTeams: "[]",
		Approvals:     "[]",
		Raw:           "{}",
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
}

// ListPRs returns tracked pull requests matching the filter, ordered by
// repository and then number.
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
	query += " ORDER BY repo, number"

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
	return where, args
}
