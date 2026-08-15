// Package metrics is what roz exposes about itself, for debugging a long-lived
// serve.
//
// Its own registry rather than prometheus's default one. The default is
// package-global and anything linked in can register against it, which for a
// binary that embeds a database driver means the exposition is whatever the
// dependency tree happened to bring — so what /metrics carries is declared
// here instead, and adding to it is a deliberate act.
//
// The Go and process collectors are registered explicitly for the same reason:
// goroutines, heap and file descriptors are exactly what a "why is serve
// behaving like this" question wants, and they are worth having on purpose
// rather than by default.
package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Outcomes a GitHub request can have.
//
// Not an HTTP status, and the reason is the transport: every request goes
// through `gh api graphql`, which reports a shell exit status and a message
// rather than a response code. Inventing a status from the message would be a
// label that looks precise and is guessed. Rate limiting is separated out
// because it is the one failure that means "wait" rather than "something is
// wrong", and it is what the poll loop paces itself on.
const (
	OutcomeOK          = "ok"
	OutcomeRateLimited = "rate_limited"
	OutcomeError       = "error"
)

var (
	// githubRequests counts what roz asked GitHub for.
	//
	// Labelled by read rather than one counter per read: the reads share a
	// shape — a batched GraphQL query, once a cycle — and what a question
	// about them looks like is "which read is failing", which is a label
	// selector rather than a different metric name.
	//
	// A read that has not happened has no series at all, which is what a
	// CounterVec does by itself: a counter appears when it is first
	// incremented, and until then there is nothing to mistake for zero.
	githubRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "roz_github_requests_total",
		Help: "GitHub GraphQL requests, by which read made them and how they ended.",
	}, []string{"read", "outcome"})

	// rateLimitRemaining is the budget as GitHub last reported it.
	rateLimitRemaining = newLazyGauge(
		"roz_github_rate_limit_remaining",
		"Points left in the GitHub GraphQL budget, as of the last response.")

	rateLimitTotal = newLazyGauge(
		"roz_github_rate_limit_total",
		"The GitHub GraphQL budget, as of the last response.")

	// schemaVersion is the migration the database is at.
	//
	// Worth exposing because it is the one number that says whether a running
	// process and its database agree: a serve that started before a migration
	// keeps serving the schema it read at startup, and this is how that shows
	// up somewhere other than in a person's memory.
	schemaVersion = newLazyGauge(
		"roz_schema_version",
		"The migration version of the database this process opened.")
)

// lazyGauge is a gauge that is absent until something sets it.
//
// A plain gauge reports 0 from the moment it is registered, and for every one
// of these zero is a real and alarming value: no budget left, or a database at
// no migration. Reporting that for "nobody has said yet" would be a lie in the
// worrying direction — and an alert on it would fire on every start, which is
// the reliable way to teach somebody to ignore an alert.
//
// Absence is the honest answer, and it is the one Prometheus is built for: a
// series that does not exist is not graphed and does not satisfy a rule.
type lazyGauge struct {
	desc *prometheus.Desc

	mu    sync.Mutex
	value float64
	known bool
}

func newLazyGauge(name, help string) *lazyGauge {
	return &lazyGauge{desc: prometheus.NewDesc(name, help, nil, nil)}
}

func (g *lazyGauge) Set(v float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value, g.known = v, true
}

func (g *lazyGauge) Describe(ch chan<- *prometheus.Desc) { ch <- g.desc }

func (g *lazyGauge) Collect(ch chan<- prometheus.Metric) {
	g.mu.Lock()
	value, known := g.value, g.known
	g.mu.Unlock()
	if !known {
		return
	}
	ch <- prometheus.MustNewConstMetric(g.desc, prometheus.GaugeValue, value)
}

// registry is what /metrics serves.
var registry = newRegistry()

func newRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(
		githubRequests,
		rateLimitRemaining,
		rateLimitTotal,
		schemaVersion,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return r
}

// GitHubRequest records one request to GitHub.
func GitHubRequest(read, outcome string) {
	githubRequests.WithLabelValues(read, outcome).Inc()
}

// RateLimit records what GitHub last said about the budget.
//
// Ignored where the response carried none, which is the ordinary case for a
// failed request: keeping the previous reading is more honest than replacing
// it with a zero nobody reported.
func RateLimit(remaining, limit int) {
	if limit <= 0 {
		return
	}
	rateLimitRemaining.Set(float64(remaining))
	rateLimitTotal.Set(float64(limit))
}

// SchemaVersion records the migration level this process opened.
func SchemaVersion(version int) {
	schemaVersion.Set(float64(version))
}

// Handler serves the exposition.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// Registry is the collector set, for a test that wants to read it back.
func Registry() *prometheus.Registry { return registry }
