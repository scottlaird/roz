// Package github reads pull request state from GitHub.
//
// It shells out to `gh api graphql` rather than speaking HTTP itself, which
// borrows the user's existing authentication and adds no dependency. Nothing
// here writes to GitHub, and nothing here knows about the database.
//
// Queries are batched with GraphQL aliases: one request carries many pull
// requests. Points are not the constraint — a batch of 100 costs 4 of the
// 5000 per hour — and neither, it turns out, is request size. What fails is
// request *time*: GitHub declines to serve a query it cannot answer quickly
// enough. See the batch size constants.
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
	"unicode/utf8"
)

// ErrRateLimited means GitHub refused the request because of a rate limit,
// rather than because anything was wrong with it.
//
// It covers both shapes GitHub uses: a transport-level 429 or 403 for
// secondary limits, and an HTTP 200 whose errors array says the primary
// GraphQL budget is spent. A caller should wait rather than retry.
var ErrRateLimited = errors.New("rate limited by GitHub")

// How many entities go into one query, per kind of read.
//
// One number per read rather than one shared, because the reads do not cost
// the same thing. A pull request carries the largest field set here — reviews,
// threads, checks, timeline — and a ref query is a repository and a namespace
// that then pages internally, so its per-entity cost is not comparable to
// either. Retuning for pull requests used to retune ref polling with it, which
// was not failing.
//
// **Tuned against time, not size.** The original numbers came from finding
// where a request got too large: 150 pull requests worked and 250 returned an
// opaque 502. That is not what fails now. A batch of 42 fails three different
// ways — an HTTP 502, a stream CANCEL, and a 200 whose body says "We couldn't
// respond to your request in time" — all of them GitHub giving up on a query
// that takes too long. The ceiling that matters is somewhere below 42, not at
// 150.
//
// So the number moves when the *query* grows, not only when the tracked set
// does. Adding a field to prFields makes every entity in a batch cost more
// time, which lowers the ceiling — treat a new field as a reason to revisit
// this, and prefer measuring to reasoning about it. Points are not the
// constraint, so a smaller batch costs one more round trip and nothing else.
//
// Narrowing the poll set to what can still move (see store.PRsToPoll) is the
// other half of this, and neither replaces the other: that took the failing
// case from 42 entities to about 12, and this is what stops it coming back as
// the tracked set grows or the query gets richer.
const (
	// PRBatchSize is the one that was failing. Well under the observed
	// ceiling rather than just below it, since the ceiling moves with the
	// field set and there is no signal to back off on when it is crossed.
	PRBatchSize = 20
	// IssueBatchSize is larger because an issue is four fields and a
	// milestone. Nothing has been observed to fail here.
	IssueBatchSize = 50
	// RefBatchSize counts repository-and-namespace queries, each of which
	// pages internally to a bound. The pagination is what governs its cost,
	// so this is not comparable to the two above and is not retuned with
	// them.
	RefBatchSize = 50
)

// What each kind of request reads, for the message a failure carries.
const (
	opPullRequests = "pull requests"
	opIssues       = "issues"
	opRefs         = "refs"
)

// batch says which request failed, in the terms the caller would need to
// reproduce it: what was being read, how much of it, and where in the run.
//
// A sync makes all three kinds of request on every cycle and they share both
// their decoding and their failure messages, so without this a failed poll
// says only that some GraphQL request returned something unexpected.
type batch struct {
	op       string
	entities int
	index    int // 1-based
	total    int
	page     int // 1-based; 0 where the request does not page
}

func (b batch) String() string {
	entities := "entities"
	if b.entities == 1 {
		entities = "entity"
	}
	s := fmt.Sprintf("%s (batch %d of %d, %d %s", b.op, b.index, b.total, b.entities, entities)
	if b.page > 0 {
		s += fmt.Sprintf(", page %d", b.page)
	}
	return s + ")"
}

// batches is how many rounds of size a set of n takes.
func batches(n, size int) int { return (n + size - 1) / size }

// fail attaches the request's identity to whatever went wrong with it. The
// cause is wrapped, so a rate limit is still recognisable as one.
func (b batch) fail(err error) error {
	return fmt.Errorf("reading %s: %w", b, err)
}

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
// A non-zero exit is not treated as fatal when there is a JSON body: gh exits 1
// whenever any alias fails to resolve, and the response still carries every
// alias that did.
//
// A body that is not JSON is a different thing entirely — GitHub answering a
// batch this size with an HTML error page — and is handled as a failed
// invocation. Returning it to be parsed would report the first stray character
// and throw away both the exit status and stderr, which is where gh says a
// limit was hit: the response would then take the escalating backoff for a
// fault rather than the flat wait a limit is supposed to get.
func runGH(ctx context.Context, query string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", "graphql", "-F", "query=@-")
	cmd.Stdin = strings.NewReader(query)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	message := strings.TrimSpace(stderr.String())

	if stdout.Len() > 0 {
		if looksJSON(stdout.Bytes()) {
			return stdout.Bytes(), nil
		}
		return nil, ghFailure(err, message, stdout.Bytes())
	}
	if err != nil {
		return nil, ghFailure(err, message, nil)
	}
	return nil, fmt.Errorf("gh api graphql returned nothing")
}

// ghFailure describes an invocation that produced no usable response, keeping
// whatever gh said about why.
//
// Both stderr and the body are searched for a limit, since which one carries
// the news depends on whether GitHub refused at the transport or answered with
// a page saying so.
func ghFailure(err error, message string, body []byte) error {
	detail := message
	if len(body) > 0 {
		if detail != "" {
			detail += "; "
		}
		detail += "body: " + excerpt(body)
	}
	if mentionsRateLimit(message) || mentionsRateLimit(string(body)) {
		return fmt.Errorf("%w: %s", ErrRateLimited, detail)
	}
	if err != nil {
		return fmt.Errorf("gh api graphql: %w: %s", err, detail)
	}
	return fmt.Errorf("gh api graphql: %s", detail)
}

// looksJSON reports whether a body is worth handing to the parser. GitHub's
// GraphQL responses are always objects, so anything else is an error page.
func looksJSON(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("{"))
}

// excerptMax is how much of an unreadable body to quote. An error page can run
// to tens of kilobytes and this is a poll loop, so the excerpt is short enough
// to sit on one line; the length is reported separately so a truncated quote
// cannot be mistaken for the whole of a short body.
const excerptMax = 200

// excerpt renders the start of a body as a single line, bounded.
func excerpt(body []byte) string {
	flat := strings.Join(strings.Fields(string(body)), " ")
	if len(flat) <= excerptMax {
		return fmt.Sprintf("%q", flat)
	}
	// Cut on a rune boundary: a body can be anything, and half a character
	// renders as escapes that read like part of the content.
	cut := excerptMax
	for cut > 0 && !utf8.RuneStart(flat[cut]) {
		cut--
	}
	return fmt.Sprintf("%q… (of %d bytes)", flat[:cut], len(body))
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

	for start := 0; start < len(keys); start += PRBatchSize {
		keyBatch := keys[start:min(start+PRBatchSize, len(keys))]
		b := batch{
			op: opPullRequests, entities: len(keyBatch),
			index: start/PRBatchSize + 1, total: batches(len(keys), PRBatchSize),
		}

		query, aliases, err := buildQuery(keyBatch)
		if err != nil {
			return Result{}, b.fail(err)
		}
		body, err := c.run(ctx, query)
		if err != nil {
			return Result{}, b.fail(err)
		}
		if err := decodeInto(body, aliases, &result); err != nil {
			return Result{}, b.fail(err)
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
		return unreadable(body, err)
	}
	if response.Data == nil {
		return noData(body, response.Errors)
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

// unreadable describes a body the JSON parser would not take.
//
// The parser reports where it gave up and nothing about what it was looking
// at, which is the difference between knowing a response was not JSON and
// knowing what arrived instead.
func unreadable(body []byte, err error) error {
	return fmt.Errorf("parsing the GraphQL response: %w: %s", err, excerpt(body))
}

// noData describes a response that parsed but carried nothing.
//
// GitHub usually says why in the errors array. When it does not, the body is
// all there is to go on, so it is quoted rather than the reader being told
// only that there was no explanation.
func noData(body []byte, errs []graphQLError) error {
	summary := summarise(errs)
	if mentionsRateLimit(summary) {
		return fmt.Errorf("%w: %s", ErrRateLimited, summary)
	}
	if len(errs) == 0 {
		return fmt.Errorf("GraphQL returned no data and no error: %s", excerpt(body))
	}
	return fmt.Errorf("GraphQL returned no data: %s", summary)
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
