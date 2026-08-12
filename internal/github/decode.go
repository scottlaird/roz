package github

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PullRequest is the state of one pull request, as GitHub reports it.
//
// Names follow GitHub's vocabulary rather than the schema's columns; mapping
// happens where it is applied. An empty string means GitHub said nothing,
// which the caller must distinguish from a value it did say.
type PullRequest struct {
	Key    string
	Repo   string
	Number int64

	Title        string
	Author       string
	URL          string
	State        string
	IsDraft      bool
	BaseRef      string
	HeadSHA      string
	InMergeQueue bool

	// ReviewDecision is APPROVED, CHANGES_REQUESTED or REVIEW_REQUIRED, and
	// empty when GitHub has no opinion.
	ReviewDecision string

	// MergeStateStatus is CLEAN, BLOCKED, BEHIND, DIRTY, UNSTABLE or DRAFT.
	// UNKNOWN is normalised away to empty: it is what GitHub says about a
	// merged or closed pull request, and transiently while it computes the
	// merge commit, so it is an absence of information rather than a fact.
	MergeStateStatus string

	// ChecksState is the rollup — SUCCESS, FAILURE, PENDING — and empty when
	// the head commit has no checks at all.
	ChecksState string
	// Checks maps each context's name to its conclusion.
	Checks map[string]string

	// ReviewerTeams holds the team slugs a review was requested from.
	// Individual reviewers appear as "@login", so a person is never mistaken
	// for a team.
	ReviewerTeams []string
	// Approvals holds the logins that have approved.
	Approvals []string

	FirstReviewRequestedAt string
	HumanCommentedAt       string

	// UnresolvedThreads counts review threads that are unresolved and not
	// outdated. Outdated means the thread hangs off a commit that is no
	// longer the head, which is GitHub's way of saying it has been overtaken
	// — close enough to "newer than head_sha" to answer the same question.
	UnresolvedThreads int
}

const (
	// unknownMergeState is GitHub saying it has not worked the merge state
	// out, or that the question no longer applies.
	unknownMergeState = "UNKNOWN"
	// approvedReview is the review state that counts as an approval.
	approvedReview = "APPROVED"
)

// The types below mirror the GraphQL response. They exist only to be
// unmarshalled into.

type wireActor struct {
	Login string `json:"login"`
	// TypeName distinguishes a Bot from a User. Asked for only where it is
	// read, so it is empty on the actors nothing tests.
	TypeName string `json:"__typename"`
}

type wireReviewer struct {
	TypeName string `json:"__typename"`
	Slug     string `json:"slug"`
	Login    string `json:"login"`
}

type wireCheckContext struct {
	TypeName string `json:"__typename"`
	// A CheckRun reports name and conclusion.
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	// A StatusContext reports context and state. They answer the same
	// question, so they are folded together.
	Context string `json:"context"`
	State   string `json:"state"`
}

type wireRollup struct {
	State    string `json:"state"`
	Contexts struct {
		Nodes []wireCheckContext `json:"nodes"`
	} `json:"contexts"`
}

type wireCommit struct {
	Commit struct {
		StatusCheckRollup *wireRollup `json:"statusCheckRollup"`
	} `json:"commit"`
}

type wirePullRequest struct {
	Number         int64  `json:"number"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	State          string `json:"state"`
	IsDraft        bool   `json:"isDraft"`
	BaseRefName    string `json:"baseRefName"`
	HeadRefOid     string `json:"headRefOid"`
	IsInMergeQueue bool   `json:"isInMergeQueue"`

	ReviewDecision   string     `json:"reviewDecision"`
	MergeStateStatus string     `json:"mergeStateStatus"`
	Author           *wireActor `json:"author"`

	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *wireReviewer `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`

	LatestOpinionatedReviews struct {
		Nodes []struct {
			State  string     `json:"state"`
			Author *wireActor `json:"author"`
		} `json:"nodes"`
	} `json:"latestOpinionatedReviews"`

	TimelineItems struct {
		Nodes []struct {
			CreatedAt string `json:"createdAt"`
		} `json:"nodes"`
	} `json:"timelineItems"`

	Comments struct {
		Nodes []struct {
			CreatedAt string     `json:"createdAt"`
			Author    *wireActor `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`

	Reviews struct {
		Nodes []struct {
			CreatedAt string     `json:"createdAt"`
			State     string     `json:"state"`
			Author    *wireActor `json:"author"`
		} `json:"nodes"`
	} `json:"reviews"`

	Commits struct {
		Nodes []wireCommit `json:"nodes"`
	} `json:"commits"`

	ReviewThreads struct {
		Nodes []struct {
			IsResolved bool `json:"isResolved"`
			IsOutdated bool `json:"isOutdated"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

type wireAlias struct {
	PullRequest *wirePullRequest `json:"pullRequest"`
}

// decodePullRequest turns one alias's payload into a PullRequest.
//
// A null pullRequest is the ordinary case for a number that does not name one
// — an issue, a deleted pull request, or one this token cannot see — and is
// reported as missing rather than as a failure.
func decodePullRequest(raw json.RawMessage, key string) (PullRequest, error) {
	var alias wireAlias
	if err := json.Unmarshal(raw, &alias); err != nil {
		return PullRequest{}, fmt.Errorf("could not be parsed: %w", err)
	}
	if alias.PullRequest == nil {
		return PullRequest{}, fmt.Errorf("is not a pull request GitHub will show us")
	}
	p := alias.PullRequest

	repo, _, found := strings.Cut(key, "#")
	if !found {
		return PullRequest{}, fmt.Errorf("%q is not a pull request key", key)
	}

	pr := PullRequest{
		Key:              key,
		Repo:             repo,
		Number:           p.Number,
		Title:            p.Title,
		URL:              p.URL,
		State:            p.State,
		IsDraft:          p.IsDraft,
		BaseRef:          p.BaseRefName,
		HeadSHA:          p.HeadRefOid,
		InMergeQueue:     p.IsInMergeQueue,
		ReviewDecision:   p.ReviewDecision,
		MergeStateStatus: mergeState(p.MergeStateStatus),
		ReviewerTeams:    reviewers(p),
		Approvals:        approvals(p),
	}
	if p.Author != nil {
		pr.Author = p.Author.Login
	}
	if len(p.TimelineItems.Nodes) > 0 {
		pr.FirstReviewRequestedAt = p.TimelineItems.Nodes[0].CreatedAt
	}
	pr.HumanCommentedAt = humanCommentedAt(p)
	pr.ChecksState, pr.Checks = checks(p)
	pr.UnresolvedThreads = unresolvedThreads(p)

	return pr, nil
}

func mergeState(status string) string {
	if status == unknownMergeState {
		return ""
	}
	return status
}

func reviewers(p *wirePullRequest) []string {
	var teams []string
	for _, node := range p.ReviewRequests.Nodes {
		r := node.RequestedReviewer
		switch {
		case r == nil:
			continue
		case r.Slug != "":
			teams = append(teams, r.Slug)
		case r.Login != "":
			teams = append(teams, "@"+r.Login)
		}
	}
	sort.Strings(teams)
	return teams
}

func approvals(p *wirePullRequest) []string {
	var logins []string
	for _, node := range p.LatestOpinionatedReviews.Nodes {
		if node.State == approvedReview && node.Author != nil {
			logins = append(logins, node.Author.Login)
		}
	}
	sort.Strings(logins)
	return logins
}

// unresolvedThreads counts the review threads that still apply: unresolved,
// and not left behind by a newer commit.
func unresolvedThreads(p *wirePullRequest) int {
	var count int
	for _, thread := range p.ReviewThreads.Nodes {
		if !thread.IsResolved && !thread.IsOutdated {
			count++
		}
	}
	return count
}

// checks flattens the rollup into a state and a name-to-conclusion map.
func checks(p *wirePullRequest) (string, map[string]string) {
	if len(p.Commits.Nodes) == 0 {
		return "", nil
	}
	rollup := p.Commits.Nodes[0].Commit.StatusCheckRollup
	if rollup == nil {
		return "", nil
	}

	byName := make(map[string]string, len(rollup.Contexts.Nodes))
	for _, c := range rollup.Contexts.Nodes {
		switch {
		case c.Name != "":
			byName[c.Name] = c.Conclusion
		case c.Context != "":
			byName[c.Context] = c.State
		}
	}
	return rollup.State, byName
}

// botType is what GraphQL calls an actor that is an integration rather than a
// person.
//
// The type, not the login. A `[bot]` suffix is a convention rather than a
// guarantee, and plenty of integrations comment as ordinary users — but
// anything GitHub itself calls a Bot certainly is one. A Mannequin, which is
// how an import represents somebody who never joined, stands for a real person
// and is left alone.
const botType = "Bot"

// pendingReview is a review its author has not submitted. Nobody else can see
// it, so it is not evidence that anybody read anything.
const pendingReview = "PENDING"

// humanCommentedAt is when somebody other than the author last said something.
//
// The name is the specification: not the newest comment, but the newest one
// from a person who is not the author. Both exclusions matter, and for the same
// reason — the field decides `frozen`, which decides whether amending is safe.
// An author's own comment is not somebody having read the commits, and a bot
// replying to itself four times in as many minutes is not either. Counting
// them freezes a pull request against its own author on the strength of
// something the author caused.
//
// Reviews count as well as issue comments, and are the stronger signal of the
// two: leaving a review means having read the code, where an issue comment may
// be about anything. A pull request whose only engagement is an inline review
// is exactly the case where amending would rewrite what a reviewer has already
// read, so excluding reviews would miss the freeze where it matters most.
//
// An actor GitHub reports as null counts. It cannot be checked against the
// author, so this is a guess — and it is made in the direction that freezes,
// because a freeze that should not have happened costs an extra commit while a
// missing one rewrites something somebody has already read.
//
// Timestamps compare as text, which is sound here: GitHub returns RFC-3339 in
// UTC with no fractional part, so the lexical and chronological orders agree.
func humanCommentedAt(p *wirePullRequest) string {
	var author string
	if p.Author != nil {
		author = p.Author.Login
	}

	newest := ""
	consider := func(at string, actor *wireActor) {
		if at <= newest || !isSomebodyElse(actor, author) {
			return
		}
		newest = at
	}

	for _, c := range p.Comments.Nodes {
		consider(c.CreatedAt, c.Author)
	}
	for _, r := range p.Reviews.Nodes {
		if r.State == pendingReview {
			continue
		}
		consider(r.CreatedAt, r.Author)
	}
	return newest
}

// isSomebodyElse reports whether an actor is a person other than the pull
// request's author.
func isSomebodyElse(actor *wireActor, author string) bool {
	if actor == nil {
		return true
	}
	return actor.TypeName != botType && actor.Login != author
}
