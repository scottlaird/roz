// Package github reads pull request state from GitHub.
//
// It shells out to `gh api graphql` rather than speaking HTTP itself, which
// borrows the user's existing authentication and adds no dependency. Nothing
// here writes to GitHub, and nothing here knows about the database.
//
// Queries are batched with GraphQL aliases: one request carries many pull
// requests. Measured against the real API, a batch of 100 costs 4 points of
// the 5000 per hour, so the budget is not the constraint — the request size
// is. Batches beyond roughly 150 fail with an opaque HTTP 502 rather than a
// useful error, hence BatchSize.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrRateLimited means GitHub refused the request because of a rate limit,
// rather than because anything was wrong with it.
//
// It covers both shapes GitHub uses: a transport-level 429 or 403 for
// secondary limits, and an HTTP 200 whose errors array says the primary
// GraphQL budget is spent. A caller should wait rather than retry.
var ErrRateLimited = errors.New("rate limited by GitHub")

// BatchSize is how many pull requests go into one query.
//
// Deliberately well under where the API starts failing: 150 works and 250
// returns a 502 with no explanation, so there is no signal to back off on.
const BatchSize = 50

// Runner executes a GraphQL query and returns the raw response body.
//
// It exists so the client can be tested without the network. Note the
// contract: a partial failure is normal, so a Runner must return the body
// even when the command exits non-zero — `gh` does that whenever any alias
// fails to resolve, while the rest of the response is perfectly good.
type Runner func(ctx context.Context, query string) ([]byte, error)

// Client reads pull requests from GitHub.
type Client struct {
	run Runner
}

// New returns a Client that shells out to gh.
func New() *Client {
	return &Client{run: runGH}
}

// NewWithRunner returns a Client backed by a custom Runner, for tests.
func NewWithRunner(run Runner) *Client {
	return &Client{run: run}
}

// runGH invokes `gh api graphql`, feeding the query on stdin so its length is
// not bounded by the argument list.
//
// A non-zero exit is not treated as fatal when there is a body: gh exits 1
// whenever any alias fails to resolve, and the response still carries every
// alias that did.
func runGH(ctx context.Context, query string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", "graphql", "-F", "query=@-")
	cmd.Stdin = strings.NewReader(query)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if stdout.Len() > 0 {
		return stdout.Bytes(), nil
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if mentionsRateLimit(message) {
			return nil, fmt.Errorf("%w: %s", ErrRateLimited, message)
		}
		return nil, fmt.Errorf("gh api graphql: %w: %s", err, message)
	}
	return nil, fmt.Errorf("gh api graphql returned nothing")
}

// RateLimit is what GitHub said about the budget on the last request.
//
// Remaining is the figure worth pacing against: the GraphQL budget is points
// per hour, not requests, and Cost is what the last query spent.
type RateLimit struct {
	Cost      int
	Remaining int
	Limit     int
	ResetAt   time.Time
}

// Known reports whether GitHub actually told us anything.
func (r RateLimit) Known() bool { return r.Limit > 0 }

// Result is what a Fetch produced: the pull requests that resolved, and the
// keys that did not.
//
// Missing is not an error. A pull request can be deleted, transferred, or
// invisible to this token, and the rest of the batch is still worth having.
type Result struct {
	PullRequests []PullRequest
	Missing      map[string]string // key → why

	// RateLimit is from the last batch, which is the most recent reading and
	// so the lowest Remaining.
	RateLimit RateLimit
}

// Fetch reads the given pull requests, in batches.
//
// An error means no data at all — a transport or authentication failure.
// Anything narrower shows up in Result.Missing.
func (c *Client) Fetch(ctx context.Context, keys []string) (Result, error) {
	result := Result{Missing: map[string]string{}}

	for start := 0; start < len(keys); start += BatchSize {
		batch := keys[start:min(start+BatchSize, len(keys))]

		query, aliases, err := buildQuery(batch)
		if err != nil {
			return Result{}, err
		}
		body, err := c.run(ctx, query)
		if err != nil {
			return Result{}, err
		}
		if err := decodeInto(body, aliases, &result); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

// graphQLError is one entry from the response's errors array. A response can
// carry both data and errors at once.
type graphQLError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Path    []any  `json:"path"`
}

type graphQLResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []graphQLError             `json:"errors"`
}

type wireRateLimit struct {
	Cost      int    `json:"cost"`
	Remaining int    `json:"remaining"`
	Limit     int    `json:"limit"`
	ResetAt   string `json:"resetAt"`
}

// decodeInto merges one response into result.
func decodeInto(body []byte, aliases map[string]string, result *Result) error {
	var response graphQLResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("parsing the GraphQL response: %w", err)
	}
	if response.Data == nil {
		summary := summarise(response.Errors)
		if mentionsRateLimit(summary) {
			return fmt.Errorf("%w: %s", ErrRateLimited, summary)
		}
		return fmt.Errorf("GraphQL returned no data: %s", summary)
	}

	if raw, ok := response.Data["rateLimit"]; ok {
		result.RateLimit = decodeRateLimit(raw)
	}

	for alias, key := range aliases {
		raw, ok := response.Data[alias]
		if !ok {
			result.Missing[key] = "GitHub returned nothing for it"
			continue
		}
		pr, err := decodePullRequest(raw, key)
		if err != nil {
			result.Missing[key] = err.Error()
			continue
		}
		result.PullRequests = append(result.PullRequests, pr)
	}
	return nil
}

func decodeRateLimit(raw json.RawMessage) RateLimit {
	var w wireRateLimit
	if err := json.Unmarshal(raw, &w); err != nil {
		return RateLimit{}
	}
	limit := RateLimit{Cost: w.Cost, Remaining: w.Remaining, Limit: w.Limit}
	if resetAt, err := time.Parse(time.RFC3339, w.ResetAt); err == nil {
		limit.ResetAt = resetAt
	}
	return limit
}

// rateLimitSignals are how GitHub says "too fast" through gh's error output
// or a GraphQL error message. Matching on text is unlovely, but gh reports
// the status in prose and there is nothing more structured to key off.
var rateLimitSignals = []string{
	"http 429",
	"rate limit",
	"secondary rate",
	"abuse detection",
}

func mentionsRateLimit(message string) bool {
	lowered := strings.ToLower(message)
	for _, signal := range rateLimitSignals {
		if strings.Contains(lowered, signal) {
			return true
		}
	}
	return false
}

func summarise(errs []graphQLError) string {
	if len(errs) == 0 {
		return "no error given"
	}
	messages := make([]string, 0, len(errs))
	for _, e := range errs {
		messages = append(messages, e.Message)
	}
	return strings.Join(messages, "; ")
}
