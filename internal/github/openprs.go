package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ReadOpenPullRequests labels the listing of a repository's open pull
// requests, which is its own read: it is paged over a whole repository rather
// than keyed by pull request.
const ReadOpenPullRequests = "open_pull_requests"

// openPRPage caps one round trip. Each pull request carries up to a page of
// files, so this is kept well under GitHub's node limit rather than at 100.
const openPRPage = 25

// openPRMaxPages bounds the listing at the 1,000 results GitHub's search will
// return. Hitting it is an error rather than a truncation: a queue missing pull
// requests says nothing is waiting that is.
const openPRMaxPages = 40

// OpenPR is an open pull request with what it takes to say who it is waiting
// on.
type OpenPR struct {
	Key   string
	URL   string
	Title string
	// Body is the description, kept for the references it carries.
	Body           string
	Author         string
	Draft          bool
	BaseRef        string
	ReviewDecision string
	CreatedAt      time.Time

	// RequestedUsers and RequestedTeams are the outstanding review requests;
	// teams are org/slug, lower case.
	RequestedUsers []string
	RequestedTeams []string
	Assignees      []string
	// Reviews is each reviewer's latest review, comments included.
	Reviews []Review
	// ChangesRequestedBy are the reviewers whose change request still stands.
	// Read separately from Reviews: a reviewer who comments after requesting
	// changes has a comment as their latest review, but the request holds.
	ChangesRequestedBy []string
	// Commenters are the people who have commented on the conversation, most
	// recent 50 comments, bots left out.
	Commenters []string

	// Files is every path the pull request touches, across as many pages as
	// it took.
	Files []string

	// LastActor and LastActivity are the most recent commit, review or
	// comment, for when nothing else says who has the next move.
	LastActor    string
	LastActivity time.Time
}

// Review is one reviewer's latest review.
type Review struct {
	Author string
	State  string
	At     time.Time
}

// OpenPullRequests lists a repository's open pull requests that are ready for
// review, with their files. repo is owner/name.
//
// Drafts and approved pull requests are left out by the search itself, so
// their files are never read: neither is waiting on a reviewer. progress, if
// set, is called after each page with the running count.
func (c *Client) OpenPullRequests(ctx context.Context, repo string, progress func(read int)) ([]OpenPR, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("repository %q is not owner/name", repo)
	}

	var prs []OpenPR
	cursor := ""
	for page := 0; ; page++ {
		if page >= openPRMaxPages {
			return nil, fmt.Errorf("%s still had open pull requests after %d pages", repo, openPRMaxPages)
		}
		batch, next, err := c.openPRPage(ctx, owner, name, cursor)
		if err != nil {
			return nil, err
		}
		prs = append(prs, batch...)
		if progress != nil {
			progress(len(prs))
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return prs, nil
}

// Codeowners reads a repository's CODEOWNERS from ref, returning "" when it
// has none.
func (c *Client) Codeowners(ctx context.Context, repo, ref string) (text, path string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return "", "", fmt.Errorf("repository %q is not owner/name", repo)
	}
	return c.codeowners(ctx, owner, name, ref)
}

type wireOpenPR struct {
	Number         int64      `json:"number"`
	URL            string     `json:"url"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	IsDraft        bool       `json:"isDraft"`
	BaseRefName    string     `json:"baseRefName"`
	ReviewDecision string     `json:"reviewDecision"`
	CreatedAt      time.Time  `json:"createdAt"`
	Author         *wireActor `json:"author"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *struct {
				Login        string `json:"login"`
				CombinedSlug string `json:"combinedSlug"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Assignees struct {
		Nodes []wireActor `json:"nodes"`
	} `json:"assignees"`
	LatestReviews struct {
		Nodes []struct {
			State       string     `json:"state"`
			SubmittedAt time.Time  `json:"submittedAt"`
			Author      *wireActor `json:"author"`
		} `json:"nodes"`
	} `json:"latestReviews"`
	LatestOpinionatedReviews struct {
		Nodes []struct {
			State  string     `json:"state"`
			Author *wireActor `json:"author"`
		} `json:"nodes"`
	} `json:"latestOpinionatedReviews"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				CommittedDate time.Time `json:"committedDate"`
				Author        struct {
					User *wireActor `json:"user"`
				} `json:"author"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Comments struct {
		Nodes []struct {
			CreatedAt time.Time  `json:"createdAt"`
			Author    *wireActor `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`
	Files struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
}

func (c *Client) openPRPage(ctx context.Context, owner, name, after string) ([]OpenPR, string, error) {
	cursor := "null"
	if after != "" {
		cursor = fmt.Sprintf("%q", after)
	}
	query := fmt.Sprintf(`query {
  rateLimit { cost remaining limit resetAt }
    search(query: %q, type: ISSUE, first: %d, after: %s) {
      pageInfo { hasNextPage endCursor }
      nodes { ... on PullRequest {
        number url title body isDraft baseRefName reviewDecision createdAt
        author { login }
        reviewRequests(first: 50) { nodes { requestedReviewer {
          ... on User { login }
          ... on Team { combinedSlug }
        } } }
        assignees(first: 20) { nodes { login } }
        latestReviews(first: 50) { nodes { state submittedAt author { login __typename } } }
        latestOpinionatedReviews(first: 50) { nodes { state author { login __typename } } }
        commits(last: 1) { nodes { commit { committedDate author { user { login } } } } }
        comments(last: 50) { nodes { createdAt author { login __typename } } }
        files(first: %d) {
          pageInfo { hasNextPage endCursor }
          nodes { path }
        }
      } }
    }
}
`, fmt.Sprintf("repo:%s/%s is:pr is:open draft:false -review:approved sort:created-asc", owner, name),
		openPRPage, cursor, filePage)

	body, err := c.request(ctx, ReadOpenPullRequests, query)
	if err != nil {
		return nil, "", err
	}
	var decoded struct {
		Data struct {
			RateLimit json.RawMessage `json:"rateLimit"`
			Search    *struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []wireOpenPR `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, "", fmt.Errorf("decoding open pull requests: %w", err)
	}
	if decoded.Data.Search == nil {
		return nil, "", fmt.Errorf("%s/%s could not be searched: %s", owner, name, firstMessage(decoded.Errors))
	}
	observeRateLimit(decodeRateLimit(decoded.Data.RateLimit))

	list := decoded.Data.Search
	prs := make([]OpenPR, 0, len(list.Nodes))
	for _, w := range list.Nodes {
		if w.Number == 0 {
			continue // a search hit that is not a pull request
		}
		pr := w.decode(owner, name)
		// The first page of files came with the listing; the rest are read
		// one pull request at a time, which only a very large change needs.
		for more, fileCursor := w.Files.PageInfo.HasNextPage, w.Files.PageInfo.EndCursor; more; {
			page, err := c.filePage(ctx, owner, name, w.Number, fileCursor)
			if err != nil {
				return nil, "", fmt.Errorf("reading %s files: %w", pr.Key, err)
			}
			pr.Files = append(pr.Files, page.Files...)
			more, fileCursor = page.HasMore, page.Cursor
		}
		prs = append(prs, pr)
	}

	next := ""
	if list.PageInfo.HasNextPage {
		next = list.PageInfo.EndCursor
	}
	return prs, next, nil
}

func (w wireOpenPR) decode(owner, name string) OpenPR {
	pr := OpenPR{
		Key:            fmt.Sprintf("%s/%s#%d", owner, name, w.Number),
		URL:            w.URL,
		Title:          w.Title,
		Body:           w.Body,
		Draft:          w.IsDraft,
		BaseRef:        w.BaseRefName,
		ReviewDecision: w.ReviewDecision,
		CreatedAt:      w.CreatedAt,
	}
	if w.Author != nil {
		pr.Author = w.Author.Login
	}
	for _, n := range w.ReviewRequests.Nodes {
		switch r := n.RequestedReviewer; {
		case r == nil:
		case r.CombinedSlug != "":
			pr.RequestedTeams = append(pr.RequestedTeams, strings.ToLower(r.CombinedSlug))
		case r.Login != "":
			pr.RequestedUsers = append(pr.RequestedUsers, r.Login)
		}
	}
	for _, a := range w.Assignees.Nodes {
		pr.Assignees = append(pr.Assignees, a.Login)
	}
	// Every reviewer's latest review, comments included: a reviewer who only
	// left comments has still picked the pull request up.
	for _, r := range w.LatestReviews.Nodes {
		// A bot's review is a check result, not a reviewer's opinion: a
		// "changes requested" from CI does not put a pull request back with
		// its author the way a person's does. A pending review hasn't been
		// sent, and a dismissed one has been taken back.
		if (r.Author != nil && r.Author.TypeName == "Bot") || r.State == "PENDING" || r.State == "DISMISSED" {
			continue
		}
		review := Review{State: r.State, At: r.SubmittedAt}
		if r.Author != nil {
			review.Author = r.Author.Login
		}
		pr.Reviews = append(pr.Reviews, review)
		pr.noteActivity(review.Author, review.At)
	}
	for _, n := range w.Commits.Nodes {
		login := ""
		if n.Commit.Author.User != nil {
			login = n.Commit.Author.User.Login
		}
		pr.noteActivity(login, n.Commit.CommittedDate)
	}
	for _, r := range w.LatestOpinionatedReviews.Nodes {
		if r.State == "CHANGES_REQUESTED" && r.Author != nil && r.Author.TypeName != "Bot" {
			pr.ChangesRequestedBy = append(pr.ChangesRequestedBy, r.Author.Login)
		}
	}
	seen := map[string]bool{}
	for _, n := range w.Comments.Nodes {
		if n.Author == nil || n.Author.TypeName == "Bot" {
			continue
		}
		login := n.Author.Login
		if !seen[login] {
			seen[login] = true
			pr.Commenters = append(pr.Commenters, login)
		}
		pr.noteActivity(login, n.CreatedAt)
	}
	for _, n := range w.Files.Nodes {
		pr.Files = append(pr.Files, n.Path)
	}
	return pr
}

func (pr *OpenPR) noteActivity(login string, at time.Time) {
	if login == "" || !at.After(pr.LastActivity) {
		return
	}
	pr.LastActor, pr.LastActivity = login, at
}
