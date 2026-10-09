package github

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// retryDelays are the waits before each retry of a request that got no
// answer: a timeout, a reset connection, a 502. GitHub drops these at random
// often enough that a one-shot command failing on the first is the common
// case, and a second try a few seconds later usually lands.
//
// The list is short on purpose. A batch GitHub cannot answer in time fails
// the same way every time; three tries cost seconds, and anything longer is
// the poll loop's backoff to decide, not this.
var retryDelays = []time.Duration{2 * time.Second, 5 * time.Second}

// Retrying returns a Runner that retries run when a request fails without a
// response. A rate limit is not retried, since the answer to one is to wait
// for GitHub, and neither is a cancelled context.
func Retrying(run Runner) Runner {
	return func(ctx context.Context, query string) ([]byte, error) {
		for attempt := 0; ; attempt++ {
			out, err := run(ctx, query)
			if err == nil || errors.Is(err, ErrRateLimited) || ctx.Err() != nil {
				return out, err
			}
			if attempt == len(retryDelays) {
				return nil, fmt.Errorf("%w (after %d attempts)", err, attempt+1)
			}
			select {
			case <-time.After(retryDelays[attempt]):
			case <-ctx.Done():
				return nil, err
			}
		}
	}
}
