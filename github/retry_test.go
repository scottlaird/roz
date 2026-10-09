package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func withoutRetryDelays(t *testing.T) {
	t.Helper()
	saved := retryDelays
	retryDelays = []time.Duration{0, 0}
	t.Cleanup(func() { retryDelays = saved })
}

func failingFor(n int, err error) (Runner, *int) {
	calls := 0
	return func(context.Context, string) ([]byte, error) {
		calls++
		if calls <= n {
			return nil, err
		}
		return []byte(`{"data":{}}`), nil
	}, &calls
}

func TestRetryingRecoversFromATransientFailure(t *testing.T) {
	withoutRetryDelays(t)
	run, calls := failingFor(2, errors.New("gh api graphql: exit status 1: gh: HTTP 502"))
	out, err := Retrying(run)(context.Background(), "q")
	if err != nil || string(out) != `{"data":{}}` || *calls != 3 {
		t.Errorf("got %q, %v after %d calls; want the answer on the third", out, err, *calls)
	}
}

func TestRetryingGivesUp(t *testing.T) {
	withoutRetryDelays(t)
	cause := errors.New("read: operation timed out")
	run, calls := failingFor(10, cause)
	_, err := Retrying(run)(context.Background(), "q")
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "after 3 attempts") || *calls != 3 {
		t.Errorf("got %v after %d calls; want the cause after 3 attempts", err, *calls)
	}
}

func TestRetryingLeavesRateLimitsAlone(t *testing.T) {
	withoutRetryDelays(t)
	run, calls := failingFor(10, fmt.Errorf("%w: HTTP 429", ErrRateLimited))
	if _, err := Retrying(run)(context.Background(), "q"); !errors.Is(err, ErrRateLimited) || *calls != 1 {
		t.Errorf("got %v after %d calls; want the rate limit at once", err, *calls)
	}
}

func TestRetryingStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run, calls := failingFor(10, errors.New("HTTP 502"))
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, err := Retrying(run)(ctx, "q"); err == nil || *calls != 1 || time.Since(start) > time.Second {
		t.Errorf("got %v after %d calls in %s; want the first failure, promptly", err, *calls, time.Since(start))
	}
}
