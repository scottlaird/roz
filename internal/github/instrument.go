package github

import (
	"context"
	"errors"

	"github.com/scottlaird/roz/internal/metrics"
)

// The reads, as they are labelled in the exposition.
//
// Named for what is being asked about rather than for the function asking, so
// a metric outlives a refactor of the caller. They are the same divisions the
// partial-failure reporting uses, which is deliberate: "which read is failing"
// should have one answer whether it is asked of a log line or of a scrape.
const (
	ReadPullRequests = "pull_requests"
	ReadIssues       = "issues"
	ReadRefs         = "refs"
	ReadTeams        = "teams"
	ReadChange       = "change"
	ReadCodeowners   = "codeowners"
)

// request runs a query and records how it went.
//
// Wrapping every call site rather than the Runner itself, because the Runner
// does not know what it is being asked — and "which read is failing" is the
// question the counter exists to answer. The wrapper returns exactly what the
// Runner did, so a caller's error handling is unchanged: several of them
// deliberately parse a body that came back with an error.
func (c *Client) request(ctx context.Context, read, query string) ([]byte, error) {
	body, err := c.run(ctx, query)
	metrics.GitHubRequest(read, outcomeOf(err))
	return body, err
}

// outcomeOf classifies what happened, keeping rate limiting apart from
// everything else: it is the one failure that means "wait" rather than
// "something is wrong", and it is what the poll loop paces itself on.
func outcomeOf(err error) string {
	switch {
	case err == nil:
		return metrics.OutcomeOK
	case errors.Is(err, ErrRateLimited):
		return metrics.OutcomeRateLimited
	default:
		return metrics.OutcomeError
	}
}

// observeRateLimit records what GitHub said about the budget, where it said
// anything. A response that carried no rate limit leaves the last reading
// alone: zero is a real and alarming value, so reporting it for "nobody said"
// would be a lie in the worrying direction.
func observeRateLimit(limit RateLimit) {
	if !limit.Known() {
		return
	}
	metrics.RateLimit(limit.Remaining, limit.Limit)
}
