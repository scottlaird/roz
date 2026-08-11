package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blocking runs until cancelled, recording that it stopped.
type blocking struct {
	name    string
	stopped atomic.Bool
	err     error
	after   time.Duration
}

func (b *blocking) Name() string { return b.name }

func (b *blocking) Run(ctx context.Context) error {
	if b.err != nil {
		if b.after > 0 {
			select {
			case <-ctx.Done():
				b.stopped.Store(true)
				return nil
			case <-time.After(b.after):
			}
		}
		return b.err
	}
	<-ctx.Done()
	b.stopped.Store(true)
	return nil
}

func TestRunStopsEveryServiceOnCancel(t *testing.T) {
	a := &blocking{name: "a"}
	b := &blocking{name: "b"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, a, b) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	if err := <-done; err != nil {
		t.Errorf("Run() returned error on cancellation: %v", err)
	}
	if !a.stopped.Load() || !b.stopped.Load() {
		t.Error("not every service was stopped")
	}
}

// TestFirstFailureStopsTheRest is the reason this exists: a component that
// cannot continue should not leave the others running against a half-working
// system.
func TestFirstFailureStopsTheRest(t *testing.T) {
	failing := &blocking{name: "failing", err: errors.New("cannot continue"), after: 10 * time.Millisecond}
	other := &blocking{name: "other"}

	err := Run(context.Background(), failing, other)
	if err == nil {
		t.Fatal("Run() returned nil, want the failure")
	}
	if !strings.Contains(err.Error(), "failing: cannot continue") {
		t.Errorf("error = %v, want it named after the service", err)
	}
	if !other.stopped.Load() {
		t.Error("the other service kept running after a failure")
	}
}

func TestRunWithNoServices(t *testing.T) {
	if err := Run(context.Background()); err != nil {
		t.Errorf("Run() with no services returned %v, want nil", err)
	}
}

// finished is a service with a natural end: it does its work and returns,
// the way an MCP server does when its client closes stdin.
type finished struct {
	name  string
	after time.Duration
}

func (f *finished) Name() string { return f.name }

func (f *finished) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
	case <-time.After(f.after):
	}
	return nil
}

// TestAServiceFinishingStopsTheRest: without this, a process whose only real
// service has ended is held open forever by the ones supporting it. `roz mcp`
// stopped exiting at EOF the moment it gained a second service, which is how
// this was found.
func TestAServiceFinishingStopsTheRest(t *testing.T) {
	done := &finished{name: "done", after: 10 * time.Millisecond}
	supporting := &blocking{name: "supporting"}

	returned := make(chan error, 1)
	go func() { returned <- Run(context.Background(), done, supporting) }()

	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("Run() returned %v, want nil: finishing is not failing", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after a service finished")
	}
	if !supporting.stopped.Load() {
		t.Error("the supporting service kept running after the other finished")
	}
}
