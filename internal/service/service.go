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

// A Service runs until its context is cancelled, or until it has nothing left
// to do — an MCP server whose client has closed stdin is finished, and saying
// so is not a failure.
//
// Run must return nil on cancellation: stopping on request is not a failure,
// and returning an error for it would report a clean shutdown as a broken one.
type Service interface {
	// Name identifies the service in errors and logs.
	Name() string
	Run(ctx context.Context) error
}

// Run runs every service until one of them returns or ctx is cancelled.
//
// The first to return cancels the rest, whether or not it failed. Failure is
// the obvious case — a component that cannot continue should not leave the
// others running against a half-working system — but finishing is the same
// argument from the other end: these are the pieces of one program, and a
// process whose reason for existing has ended should not be held open by the
// services that were supporting it.
//
// The returned error is the first failure; a clean shutdown returns nil.
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
			defer cancel()
			if err := s.Run(ctx); err != nil {
				failures <- fmt.Errorf("%s: %w", s.Name(), err)
			}
		}()
	}

	wg.Wait()
	close(failures)

	// Report the first failure; the rest are almost certainly the shutdown it
	// caused.
	return <-failures
}
