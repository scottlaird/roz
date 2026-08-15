package ghsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// Defaults for a Syncer. They are exported so the command's flag help and the
// code cannot disagree about them.
const (
	DefaultInterval       = time.Minute
	DefaultRateLimitFloor = 2500
	DefaultMaxFailures    = 10
	maxBackoff            = 15 * time.Minute
)

// Syncer polls GitHub on a loop.
//
// It reports nothing on a quiet cycle. The event log is the output: a change
// shows up in `roz watch`, and printing a line per poll would bury it. Only
// the exceptional gets written to Log — slowing down, backing off, failing.
type Syncer struct {
	Store  *store.Store
	Client Fetcher

	// Interval is the gap between polls when the budget is healthy.
	Interval time.Duration
	// RateLimitFloor is the remaining budget below which polling is paced out
	// to make what is left last until the limit resets.
	RateLimitFloor int
	// MaxFailures is how many consecutive failures to tolerate before giving
	// up. Zero means never give up, which suits a supervised process.
	MaxFailures int

	// Log receives the exceptional. Nil discards it.
	Log io.Writer

	// now is swappable for tests.
	now func() time.Time
}

func (s *Syncer) Name() string { return "sync:github" }

// Run polls until ctx is cancelled.
func (s *Syncer) Run(ctx context.Context) error {
	s.applyDefaults()
	s.logf("polling GitHub every %s, pacing below %d remaining", s.Interval, s.RateLimitFloor)

	var failures int
	delay := time.Duration(0) // poll immediately on start

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}

		result, err := Sync(ctx, s.Store, s.Client)
		switch {
		case ctx.Err() != nil:
			// Cancelled mid-poll; that is a stop, not a failure.
			return nil
		case err != nil:
			failures++
			delay = s.failureDelay(err, failures)
			s.logf("sync failed (%d in a row), waiting %s: %v", failures, delay, err)
			if s.MaxFailures > 0 && failures >= s.MaxFailures {
				return fmt.Errorf("gave up after %d consecutive failures: %w", failures, err)
			}
		default:
			// A cycle where some read failed and others did not is a working
			// cycle, so it resets the counter and paces normally.
			//
			// The alternative — counting it — eventually stops a process that
			// is doing most of its job, and takes the services sharing it
			// down too: a ref read that has been broken for a fortnight would
			// stop pull requests being polled at all. What that trades away
			// is the guarantee that a persistent failure ends in something
			// louder than a log line, so the log line is written every cycle
			// rather than once, and says which read it was.
			for _, failed := range result.Failed {
				s.logf("sync read %s failed, carrying on with the rest: %v",
					failed.Read, failed.Err)
			}
			failures = 0
			delay = s.nextDelay(result.RateLimit)
		}
	}
}

func (s *Syncer) applyDefaults() {
	if s.Interval <= 0 {
		s.Interval = DefaultInterval
	}
	if s.RateLimitFloor < 0 {
		s.RateLimitFloor = DefaultRateLimitFloor
	}
	if s.now == nil {
		s.now = time.Now
	}
}

// failureDelay decides how long to wait after a failed poll.
//
// A rate limit is not a fault and clears on its own schedule, so it waits the
// longest allowed rather than escalating towards it. Anything else doubles
// from the configured interval, because a failure repeating at full speed is
// its own denial of service.
//
// Backing off from the interval rather than a fixed floor means a deployment
// polling every ten minutes does not retry a failure in thirty seconds, and a
// test polling every millisecond does not have to wait.
func (s *Syncer) failureDelay(err error, failures int) time.Duration {
	if errors.Is(err, github.ErrRateLimited) {
		return maxBackoff
	}

	delay := s.Interval << min(failures, 6)
	return min(delay, maxBackoff)
}

// nextDelay paces the next poll against the remaining budget.
func (s *Syncer) nextDelay(limit github.RateLimit) time.Duration {
	delay, slowed := pace(s.Interval, limit, s.RateLimitFloor, s.now())
	if slowed {
		s.logf("rate limit at %d of %d, slowing to %s until it resets at %s",
			limit.Remaining, limit.Limit, delay, limit.ResetAt.Format(time.RFC3339))
	}
	return delay
}

// pace returns the interval to use, and whether it was stretched.
//
// Above the floor, nothing changes. Below it, the remaining budget is spread
// across the time left until it resets, so polling slows exactly enough to
// last rather than by an arbitrary multiplier. It never speeds up: the
// configured interval is a floor as well as a default.
func pace(base time.Duration, limit github.RateLimit, floor int, now time.Time) (time.Duration, bool) {
	if !limit.Known() || limit.Remaining >= floor {
		return base, false
	}

	window := limit.ResetAt.Sub(now)
	if window <= 0 {
		// The reset has passed and the next poll will see a fresh budget.
		return base, false
	}
	if limit.Remaining <= 0 {
		return window, true
	}

	// One poll costs at least a point, so this is the optimistic count.
	affordable := limit.Remaining
	if limit.Cost > 0 {
		affordable = limit.Remaining / limit.Cost
	}
	if affordable <= 0 {
		return window, true
	}

	spaced := window / time.Duration(affordable)
	if spaced <= base {
		return base, false
	}
	return spaced, true
}

func (s *Syncer) logf(format string, args ...any) {
	if s.Log == nil {
		return
	}
	fmt.Fprintf(s.Log, format+"\n", args...)
}
