// Package telemetry exposes bounded business signals through the framework's
// default Prometheus registry. Resource identities belong in logs, not labels.
package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	fileResults = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "artifact_storage_file_results_total",
			Help: "Individual file registration and confirmation results, including errors in HTTP 200 batches.",
		},
		[]string{"operation", "result"},
	)
	requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "artifact_service_requests_total",
		Help: "Completed artifact and storage HTTP requests by route template and status.",
	}, []string{"domain", "method", "route", "status"})
	artifactLocks = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "artifact_lock_acquire_seconds",
		Help:    "Elapsed time for Artifact locking queries, including database round trip and lock wait.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"operation"})
	taskFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "artifact_service_task_failures_total",
		Help: "Failed background task execution attempts by owning domain and task kind.",
	}, []string{"domain", "kind"})
)

func init() {
	prometheus.MustRegister(requests, artifactLocks, taskFailures, fileResults)
}

// Request records one completed request. Route must be a registered route
// template, never a raw URL; the other labels must be bounded classifications.
func Request(domain, method, route, status string) {
	requests.WithLabelValues(domain, method, route, status).Inc()
}

// ObserveArtifactLock records the complete locking query, not isolated database
// lock-blocking time. Operation must be a fixed operation name, never an ID.
func ObserveArtifactLock(operation string, duration time.Duration) {
	artifactLocks.WithLabelValues(operation).Observe(duration.Seconds())
}

// TaskFailure records a failed execution attempt, including retryable failures.
// Domain and kind are fixed task classifications, never error text or task IDs.
func TaskFailure(domain, kind string) {
	taskFailures.WithLabelValues(domain, kind).Inc()
}

// FileResult observes one item; operation is register or complete, not a file ID.
func FileResult(operation string, failed bool) {
	result := "success"
	if failed {
		result = "failure"
	}
	fileResults.WithLabelValues(operation, result).Inc()
}
