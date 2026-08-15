package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scrape reads the exposition the way a Prometheus server would.
func scrape(t *testing.T) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", recorder.Code)
	}
	return recorder.Body.String()
}

// TestAnUnknownGaugeIsAbsentRatherThanZero: zero is a real and alarming value
// for every gauge here — no budget left, a database at no migration — so
// reporting it for "nobody has said yet" would be a lie in the worrying
// direction, and an alert on it would fire on every start.
func TestAnUnknownGaugeIsAbsentRatherThanZero(t *testing.T) {
	gauge := newLazyGauge("roz_test_unknown", "help")
	registry := newRegistry()
	registry.MustRegister(gauge)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() returned error: %v", err)
	}
	for _, family := range families {
		if family.GetName() == "roz_test_unknown" && len(family.GetMetric()) > 0 {
			t.Errorf("an unset gauge reported %v", family.GetMetric())
		}
	}

	gauge.Set(3)
	families, err = registry.Gather()
	if err != nil {
		t.Fatalf("Gather() returned error: %v", err)
	}
	var found bool
	for _, family := range families {
		if family.GetName() == "roz_test_unknown" {
			found = true
			if got := family.GetMetric()[0].GetGauge().GetValue(); got != 3 {
				t.Errorf("gauge = %v, want 3", got)
			}
		}
	}
	if !found {
		t.Error("a set gauge is still absent")
	}
}

// TestARateLimitNobodyReportedIsIgnored: a failed request carries no budget,
// and replacing the last real reading with a zero would be worse than keeping
// a slightly old one.
func TestARateLimitNobodyReportedIsIgnored(t *testing.T) {
	RateLimit(4832, 5000)
	RateLimit(0, 0)

	body := scrape(t)
	if !strings.Contains(body, "roz_github_rate_limit_remaining 4832") {
		t.Errorf("the last real reading was lost:\n%s", body)
	}
}

// TestRequestsAreCountedByReadAndOutcome is what the issue asked for: one
// metric with enough labels to answer "which read is failing".
func TestRequestsAreCountedByReadAndOutcome(t *testing.T) {
	GitHubRequest("pull_requests", OutcomeOK)
	GitHubRequest("pull_requests", OutcomeOK)
	GitHubRequest("refs", OutcomeRateLimited)

	body := scrape(t)
	for _, want := range []string{
		`roz_github_requests_total{outcome="ok",read="pull_requests"} 2`,
		`roz_github_requests_total{outcome="rate_limited",read="refs"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s:\n%s", want, body)
		}
	}
}

// TestTheRuntimeIsExposed: goroutines and heap are what a "why is serve
// behaving like this" question actually wants, and they are the reason for
// taking the dependency rather than hand-writing the text format.
func TestTheRuntimeIsExposed(t *testing.T) {
	body := scrape(t)
	for _, want := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes"} {
		if !strings.Contains(body, want) {
			t.Errorf("the runtime collector is not registered: no %s", want)
		}
	}
}
