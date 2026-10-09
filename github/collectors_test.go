package github

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestCollectorsExportToAnotherRegistry is a program with its own registry:
// what the client records shows up there, without roz's Go and process
// collectors coming along to collide with the program's own.
func TestCollectorsExportToAnotherRegistry(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(Collectors()...)

	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return nil, errors.New("HTTP 502")
	})
	_, _ = client.SearchPullRequests(context.Background(), "is:pr org:acme", 10)

	if n, err := testutil.GatherAndCount(registry, "roz_github_requests_total"); err != nil || n == 0 {
		t.Errorf("roz_github_requests_total: %d series, %v; want the failed search", n, err)
	}
	if n, err := testutil.GatherAndCount(registry, "go_goroutines"); err != nil || n != 0 {
		t.Errorf("go_goroutines: %d series, %v; want none, it is the program's to register", n, err)
	}
}
