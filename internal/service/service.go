// Package service runs long-lived components together.
//
// It exists so that syncing, tailing the log and eventually serving the
// status page can be separate things that one command runs side by side,
// rather than one loop that grows extra jobs bolted into it.
package service

import (
	"context"
	"fmt"
	"sync"
)

// A Service runs until its context is cancelled.
//
// Run must return nil on cancellation: stopping on request is not a failure,
// and returning an error for it would take the other services down with it.
type Service interface {
	// Name identifies the service in errors and logs.
	Name() string
	Run(ctx context.Context) error
}

// Run runs every service until one fails or ctx is cancelled.
//
// The first failure cancels the rest, so a component that cannot continue
// does not leave the others running against a half-working system. The
// returned error is that first failure; a clean shutdown returns nil.
func Run(ctx context.Context, services ...Service) error {
	if len(services) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	failures := make(chan error, len(services))

	for _, s := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Run(ctx); err != nil {
				failures <- fmt.Errorf("%s: %w", s.Name(), err)
				cancel()
			}
		}()
	}

	wg.Wait()
	close(failures)

	// Report the first failure; the rest are almost certainly the shutdown it
	// caused.
	return <-failures
}
