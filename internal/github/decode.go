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
	if len(p.Comments.Nodes) > 0 {
		pr.HumanCommentedAt = p.Comments.Nodes[0].CreatedAt
	}
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
