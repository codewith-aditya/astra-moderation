// Package metrics exposes a dependency-free Prometheus text exposition
// endpoint. Counters and a coarse latency histogram are kept in memory.
package metrics

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var buckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120}

// Metrics is safe for concurrent use.
type Metrics struct {
	mu sync.Mutex

	requests map[string]uint64 // verdict -> count
	errs     uint64
	jobs     map[string]uint64 // status -> count

	histCount []uint64
	histSum   float64

	start time.Time
}

// New creates an empty metrics registry.
func New() *Metrics {
	return &Metrics{
		requests:  map[string]uint64{},
		jobs:      map[string]uint64{},
		histCount: make([]uint64, len(buckets)),
		start:     time.Now(),
	}
}

// ObserveRequest records one moderation request outcome and duration.
func (m *Metrics) ObserveRequest(verdict string, d time.Duration) {
	sec := d.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[verdict]++
	m.histSum += sec
	for i, b := range buckets {
		if sec <= b {
			m.histCount[i]++
		}
	}
}

// IncError records an upstream or internal error.
func (m *Metrics) IncError() {
	m.mu.Lock()
	m.errs++
	m.mu.Unlock()
}

// ObserveJob records a terminal job state.
func (m *Metrics) ObserveJob(status string) {
	m.mu.Lock()
	m.jobs[status]++
	m.mu.Unlock()
}

// Write renders the metrics in Prometheus text format.
func (m *Metrics) Write(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintf(w, "# HELP astra_moderation_requests_total Moderation requests by verdict.\n")
	fmt.Fprintf(w, "# TYPE astra_moderation_requests_total counter\n")
	for _, v := range []string{"safe", "review", "block"} {
		fmt.Fprintf(w, "astra_moderation_requests_total{verdict=%q} %d\n", v, m.requests[v])
	}
	fmt.Fprintf(w, "# HELP astra_moderation_upstream_errors_total Upstream failures.\n")
	fmt.Fprintf(w, "# TYPE astra_moderation_upstream_errors_total counter\n")
	fmt.Fprintf(w, "astra_moderation_upstream_errors_total %d\n", m.errs)

	fmt.Fprintf(w, "# HELP astra_moderation_request_duration_seconds Moderation latency.\n")
	fmt.Fprintf(w, "# TYPE astra_moderation_request_duration_seconds histogram\n")
	var total uint64
	for i, b := range buckets {
		total = m.histCount[i]
		fmt.Fprintf(w, "astra_moderation_request_duration_seconds_bucket{le=%q} %d\n", trimFloat(b), total)
	}
	fmt.Fprintf(w, "astra_moderation_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", m.histCount[len(buckets)-1])
	fmt.Fprintf(w, "astra_moderation_request_duration_seconds_sum %.3f\n", m.histSum)
	fmt.Fprintf(w, "astra_moderation_request_duration_seconds_count %d\n", m.histCount[len(buckets)-1])

	fmt.Fprintf(w, "# HELP astra_moderation_jobs_total Async jobs by terminal status.\n")
	fmt.Fprintf(w, "# TYPE astra_moderation_jobs_total counter\n")
	for _, s := range []string{"done", "failed"} {
		fmt.Fprintf(w, "astra_moderation_jobs_total{status=%q} %d\n", s, m.jobs[s])
	}

	fmt.Fprintf(w, "# HELP astra_moderation_uptime_seconds Process uptime.\n")
	fmt.Fprintf(w, "# TYPE astra_moderation_uptime_seconds gauge\n")
	fmt.Fprintf(w, "astra_moderation_uptime_seconds %.0f\n", time.Since(m.start).Seconds())
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
