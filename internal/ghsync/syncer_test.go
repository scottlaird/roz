package ghsync

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

func TestPace(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	base := time.Minute
	const floor = 2500

	tests := []struct {
		name       string
		limit      github.RateLimit
		want       time.Duration
		wantSlowed bool
	}{
		{
			name:  "nothing known keeps the configured interval",
			limit: github.RateLimit{},
			want:  base,
		},
		{
			name:  "healthy budget keeps the configured interval",
			limit: github.RateLimit{Limit: 5000, Remaining: 4000, Cost: 4, ResetAt: now.Add(time.Hour)},
			want:  base,
		},
		{
			name:  "exactly at the floor is still healthy",
			limit: github.RateLimit{Limit: 5000, Remaining: floor, Cost: 4, ResetAt: now.Add(time.Hour)},
			want:  base,
		},
		{
			// 400 points left at 4 a poll is 100 polls; an hour spread over
			// 100 polls is 36s, which is faster than the configured minute,
			// so the interval stands.
			name:  "below the floor but still affordable does not slow down",
			limit: github.RateLimit{Limit: 5000, Remaining: 400, Cost: 4, ResetAt: now.Add(time.Hour)},
			want:  base,
		},
		{
			// 40 points at 4 a poll is 10 polls in an hour: one every 6m.
			name:       "a thin budget is spread across the window",
			limit:      github.RateLimit{Limit: 5000, Remaining: 40, Cost: 4, ResetAt: now.Add(time.Hour)},
			want:       6 * time.Minute,
			wantSlowed: true,
		},
		{
			name:       "an exhausted budget waits for the reset",
			limit:      github.RateLimit{Limit: 5000, Remaining: 0, Cost: 4, ResetAt: now.Add(20 * time.Minute)},
			want:       20 * time.Minute,
			wantSlowed: true,
		},
		{
			name:  "a reset already past keeps the configured interval",
			limit: github.RateLimit{Limit: 5000, Remaining: 10, Cost: 4, ResetAt: now.Add(-time.Minute)},
			want:  base,
		},
		{
			// Cost is unknown on a response that carried none; assume one.
			name:       "no cost reported assumes a point a poll",
			limit:      github.RateLimit{Limit: 5000, Remaining: 10, ResetAt: now.Add(time.Hour)},
			want:       6 * time.Minute,
			wantSlowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, slowed := pace(base, tt.limit, floor, now)
			if got != tt.want || slowed != tt.wantSlowed {
				t.Errorf("pace() = %s, slowed %v; want %s, slowed %v",
					got, slowed, tt.want, tt.wantSlowed)
			}
		})
	}
}

// TestPaceNeverSpeedsUp is the property that matters most: the configured
// interval is a floor, so a healthy budget cannot make polling more frequent
// than asked for.
func TestPaceNeverSpeedsUp(t *testing.T) {
	now := time.Now()
	base := 5 * time.Minute

	limit := github.RateLimit{Limit: 5000, Remaining: 1, Cost: 1, ResetAt: now.Add(time.Second)}
	got, _ := pace(base, limit, 2500, now)
	if got < base {
		t.Errorf("pace() = %s, want at least the configured %s", got, base)
	}
}

func TestFailureDelayBacksOff(t *testing.T) {
	s := &Syncer{}

	s.Interval = time.Second

	ordinary := errors.New("network unreachable")
	first := s.failureDelay(ordinary, 1)
	second := s.failureDelay(ordinary, 2)
	if second <= first {
		t.Errorf("backoff did not grow: %s then %s", first, second)
	}

	// Growth stops rather than running away, and never passes the ceiling.
	capped := s.failureDelay(ordinary, 50)
	if capped != s.failureDelay(ordinary, 6) {
		t.Errorf("backoff still growing at 50 failures: %s", capped)
	}
	if capped > maxBackoff {
		t.Errorf("backoff = %s, want no more than %s", capped, maxBackoff)
	}

	// A long interval reaches the ceiling rather than doubling past it.
	long := &Syncer{Interval: 10 * time.Minute}
	if got := long.failureDelay(ordinary, 50); got != maxBackoff {
		t.Errorf("backoff from a 10m interval = %s, want it capped at %s", got, maxBackoff)
	}

	// A rate limit is not a fault; it waits rather than escalating.
	limited := s.failureDelay(github.ErrRateLimited, 1)
	if limited != maxBackoff {
		t.Errorf("rate limited delay = %s, want %s", limited, maxBackoff)
	}
}

// syncBuffer is an io.Writer the test can read while the syncer goroutine
// writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// countingFetcher counts polls and can be told to fail.
type countingFetcher struct {
	mu     sync.Mutex
	calls  int
	err    error
	result github.Result
}

func (c *countingFetcher) Fetch(context.Context, []string) (github.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return github.Result{}, c.err
	}
	return c.result, nil
}

// Refs is quiet: these tests are about how often the loop runs, and nothing
// in them waits on a ref, so sync never asks.
func (c *countingFetcher) Refs(context.Context, []github.RefQuery) (github.RefResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return github.RefResult{}, c.err
	}
	return github.RefResult{}, nil
}

func (c *countingFetcher) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSyncerPollsUntilCancelled(t *testing.T) {
	st, key := newStore(t)
	client := &countingFetcher{result: github.Result{
		PullRequests: []github.PullRequest{observed(key)},
	}}

	syncer := &Syncer{
		Store:    st,
		Client:   client,
		Interval: 20 * time.Millisecond,
		Log:      &syncBuffer{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- syncer.Run(ctx) }()

	if err := waitFor(func() bool { return client.count() >= 3 }); err != nil {
		t.Fatalf("syncer did not keep polling: %v (calls %d)", err, client.count())
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run() returned error on cancellation: %v", err)
	}
}

// TestSyncerGivesUpAfterRepeatedFailure keeps a broken configuration — bad
// credentials, say — from spinning silently forever.
func TestSyncerGivesUpAfterRepeatedFailure(t *testing.T) {
	st, _ := newStore(t)
	client := &countingFetcher{err: errors.New("gh: not authenticated")}

	var log syncBuffer
	syncer := &Syncer{
		Store:       st,
		Client:      client,
		Interval:    time.Millisecond,
		MaxFailures: 2,
		Log:         &log,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := syncer.Run(ctx)
	if err == nil {
		t.Fatal("Run() returned nil after repeated failure, want an error")
	}
	if !strings.Contains(err.Error(), "gave up") {
		t.Errorf("error = %v, want it to say it gave up", err)
	}
	if !strings.Contains(log.String(), "not authenticated") {
		t.Errorf("log does not carry the cause:\n%s", log.String())
	}
}

// TestSyncerIsQuiet checks the loop says nothing on an ordinary cycle: the
// event log is the output, and a line per poll would bury it.
func TestSyncerIsQuiet(t *testing.T) {
	st, key := newStore(t)
	client := &countingFetcher{result: github.Result{
		PullRequests: []github.PullRequest{observed(key)},
		RateLimit:    github.RateLimit{Limit: 5000, Remaining: 4999, Cost: 1, ResetAt: time.Now().Add(time.Hour)},
	}}

	var log syncBuffer
	syncer := &Syncer{Store: st, Client: client, Interval: 10 * time.Millisecond, Log: &log}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- syncer.Run(ctx) }()

	if err := waitFor(func() bool { return client.count() >= 3 }); err != nil {
		t.Fatalf("syncer did not poll: %v", err)
	}
	cancel()
	<-done

	// One startup line, and nothing per poll.
	lines := strings.Count(strings.TrimSpace(log.String()), "\n") + 1
	if lines > 1 {
		t.Errorf("logged %d lines over %d polls, want only the startup line:\n%s",
			lines, client.count(), log.String())
	}
}

// TestSyncerReportsSlowingDown is the other half: the exceptional is worth
// saying out loud.
func TestSyncerReportsSlowingDown(t *testing.T) {
	st, key := newStore(t)
	client := &countingFetcher{result: github.Result{
		PullRequests: []github.PullRequest{observed(key)},
		RateLimit: github.RateLimit{
			Limit: 5000, Remaining: 10, Cost: 4, ResetAt: time.Now().Add(time.Hour),
		},
	}}

	var log syncBuffer
	syncer := &Syncer{
		Store: st, Client: client,
		Interval: time.Millisecond, RateLimitFloor: 2500, Log: &log,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- syncer.Run(ctx) }()

	if err := waitFor(func() bool { return strings.Contains(log.String(), "slowing to") }); err != nil {
		t.Errorf("syncer did not report slowing down: %v\n%s", err, log.String())
	}
	cancel()
	<-done
}

func waitFor(condition func() bool) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return errors.New("timed out")
}

// TestSyncerLogsAPartialCycleEveryTime is what the give-up decision trades
// against. A cycle where some read failed and others did not resets the
// counter, so a persistently broken read never stops the process — which
// means the log line is the only thing standing between it and nobody
// noticing, so it is written every cycle rather than once.
func TestSyncerLogsAPartialCycleEveryTime(t *testing.T) {
	st, key := newStore(t)
	client := &failingRefs{
		fakeFetcher: &fakeFetcher{result: github.Result{
			PullRequests: []github.PullRequest{observed(key)},
		}},
		refErr: errors.New("gh: connection reset"),
	}
	addRefWait(t, st, store.RefWait{
		RepoID: "owner/repo", Kind: store.RefTag, Matcher: ">=1.0",
	})

	var log syncBuffer
	syncer := &Syncer{
		Store:       st,
		Client:      client,
		Interval:    time.Millisecond,
		MaxFailures: 2,
		Log:         &log,
	}
	// Waited for rather than timed. This used to run for 200ms of wall clock
	// and assert that at least two cycles had finished, which is a bet on how
	// much a machine gets through in a fifth of a second — and CI is where
	// that bet is worst: the -race job runs this package under load, and one
	// cycle in 200ms is entirely possible there. It failed on two unrelated
	// pull requests within ten minutes. See #251.
	//
	// What is being asserted is "every cycle, not once", and that is a
	// question about the second occurrence rather than about elapsed time.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- syncer.Run(ctx) }()

	twice := func() bool {
		return strings.Count(log.String(), "carrying on with the rest") >= 2
	}
	if err := waitFor(twice); err != nil {
		t.Errorf("a partial cycle was logged once and then suppressed: %v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "refs") {
		t.Errorf("the log does not say which read failed:\n%s", log.String())
	}

	// It runs until cancelled rather than giving up: the pull request read is
	// working, and stopping would take that down with the ref read.
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() gave up on a cycle that was doing most of its job: %v", err)
	}
}
