package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultGraphQLEndpoint is github.com's GraphQL API.
const DefaultGraphQLEndpoint = "https://api.github.com/graphql"

// maxResponse bounds how much of a response is read. The largest batches
// here come back in a few megabytes; anything near this is not a GraphQL
// answer.
const maxResponse = 64 << 20

// TokenSource returns the token for the next request. It is asked every
// time, so a source whose tokens expire, such as a GitHub App's installation
// tokens, can renew them.
type TokenSource func(ctx context.Context) (string, error)

// StaticToken is a TokenSource for a token that doesn't change: a personal
// access token, or one from the environment.
func StaticToken(token string) TokenSource {
	return func(context.Context) (string, error) { return token, nil }
}

// NewHTTP returns a Client that speaks to github.com's GraphQL API directly,
// authenticating with token, rather than through gh.
func NewHTTP(token string) *Client {
	return NewWithRunner(HTTPRunner(nil, DefaultGraphQLEndpoint, StaticToken(token)))
}

// HTTPRunner returns a Runner that posts each query to endpoint with a token
// from tokens. A nil hc uses http.DefaultClient; the request's deadline is
// the context's.
//
// It keeps the Runner contract the way gh does: a 200 whose errors array
// names some aliases is returned whole, because the rest of it is good. Any
// other status is a failure, and a rate limit is reported as ErrRateLimited
// whether GitHub said so in the status, the headers or the body.
//
// After a rate limit, requests through the same runner fail at once with
// ErrRateLimited until GitHub's wait is over, rather than adding to the count
// GitHub is holding against the token. The wait is Retry-After, the budget's
// reset time, or a minute when GitHub gives neither.
func HTTPRunner(hc *http.Client, endpoint string, tokens TokenSource) Runner {
	if hc == nil {
		hc = http.DefaultClient
	}
	var hold backoff
	return func(ctx context.Context, query string) ([]byte, error) {
		if until, held := hold.until(time.Now()); held {
			return nil, fmt.Errorf("%w: waiting until %s", ErrRateLimited, until.Format(time.RFC3339))
		}
		token, err := tokens(ctx)
		if err != nil {
			return nil, fmt.Errorf("getting a GitHub token: %w", err)
		}
		payload, err := json.Marshal(map[string]string{"query": query})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "roz")

		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("POST %s: %w", endpoint, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
		if err != nil {
			return nil, fmt.Errorf("reading the GraphQL response: %w", err)
		}
		out, err := httpResult(resp, body)
		if errors.Is(err, ErrRateLimited) {
			hold.set(limitEnds(resp, time.Now()))
		}
		return out, err
	}
}

// backoff is when a rate-limited runner may send again.
type backoff struct {
	mu  sync.Mutex
	end time.Time
}

func (b *backoff) until(now time.Time) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.end, now.Before(b.end)
}

func (b *backoff) set(end time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if end.After(b.end) {
		b.end = end
	}
}

// limitEnds is when GitHub says a rate limit is over.
func limitEnds(resp *http.Response, now time.Time) time.Time {
	if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 {
		return now.Add(time.Duration(s) * time.Second)
	}
	if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" {
		if s, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			if reset := time.Unix(s, 0); reset.After(now) {
				return reset
			}
		}
	}
	return now.Add(time.Minute)
}

// httpResult decides what a response means: a body to parse, or why there
// isn't one.
func httpResult(resp *http.Response, body []byte) ([]byte, error) {
	if resp.StatusCode == http.StatusOK && looksJSON(body) {
		return body, nil
	}
	detail := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if len(body) > 0 {
		detail += ": body: " + excerpt(body)
	}
	if limitedByHeaders(resp) || mentionsRateLimit(detail) {
		if wait := resp.Header.Get("Retry-After"); wait != "" {
			detail += "; retry after " + wait + "s"
		}
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, detail)
	}
	return nil, fmt.Errorf("GraphQL request failed: %s", detail)
}

// limitedByHeaders reports a refusal the headers explain: a 429, or a 403
// with the budget spent or a Retry-After, which is how GitHub sends a
// secondary limit. Any other 403 is a permissions problem, not a wait.
func limitedByHeaders(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return resp.Header.Get("Retry-After") != "" ||
			strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0"
	}
	return false
}
