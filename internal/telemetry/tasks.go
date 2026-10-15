package telemetry

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TaskGroup is a database-owned snapshot, without resource identifiers.
type TaskGroup struct {
	Kind, State string
	Count       int64
	OldestDue   time.Time
}

type TaskSnapshot struct {
	Groups  []TaskGroup
	Blocked int64
	Now     time.Time
}

type taskCollector struct {
	mu                           sync.Mutex
	closed                       bool
	active                       sync.WaitGroup
	ctx                          context.Context
	snapshot                     func(context.Context) (TaskSnapshot, error)
	tasks, age, success, blocked *prometheus.Desc
	kinds                        []string
}

var workerInstance atomic.Uint64

// RegisterTasks observes one running worker instance. Each scrape has a bounded
// database read; metrics failures do not change tasks or their failure budgets.
func RegisterTasks(
	ctx context.Context,
	domain string,
	kinds []string,
	snapshot func(context.Context) (TaskSnapshot, error),
) (func(), error) {
	labels := prometheus.Labels{"domain": domain, "worker_instance": strconv.FormatUint(workerInstance.Add(1), 10)}
	c := &taskCollector{
		ctx:      ctx,
		snapshot: snapshot,
		kinds:    append([]string(nil), kinds...),
		tasks: prometheus.NewDesc(
			"artifact_background_tasks",
			"Current durable tasks by state.",
			[]string{"kind", "state"},
			labels,
		),
		age: prometheus.NewDesc(
			"artifact_background_task_oldest_due_age_seconds",
			"Age of the oldest due pending task or expired running lease.",
			[]string{"kind"},
			labels,
		),
		success: prometheus.NewDesc(
			"artifact_background_task_collection_success",
			"Whether the current task database snapshot succeeded.",
			nil,
			labels,
		),
	}
	if domain == "artifact" {
		c.blocked = prometheus.NewDesc(
			"artifact_retention_blocked_artifacts",
			"Enabled retention policies whose last evaluation was blocked.",
			[]string{"reason"},
			labels,
		)
	}
	if err := prometheus.Register(c); err != nil {
		return func() {}, err
	}
	return func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		prometheus.Unregister(c)
		c.active.Wait()
	}, nil
}

func (c *taskCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.tasks
	ch <- c.age
	ch <- c.success
	if c.blocked != nil {
		ch <- c.blocked
	}
}

func (c *taskCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.active.Add(1)
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	snapshot, err := c.snapshot(ctx)
	cancel()
	c.active.Done() // No database borrowing remains while delivering metrics.

	success := float64(1)
	if err != nil {
		success = 0
	}
	ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, success)
	if err != nil {
		return
	}
	counts := map[string]map[string]int64{}
	oldest := map[string]time.Time{}
	for _, g := range snapshot.Groups {
		if counts[g.Kind] == nil {
			counts[g.Kind] = map[string]int64{}
		}
		counts[g.Kind][g.State] = g.Count
		if !g.OldestDue.IsZero() && (oldest[g.Kind].IsZero() || g.OldestDue.Before(oldest[g.Kind])) {
			oldest[g.Kind] = g.OldestDue
		}
	}
	for _, kind := range c.kinds {
		for _, state := range []string{"pending", "running", "failed"} {
			ch <- prometheus.MustNewConstMetric(c.tasks, prometheus.GaugeValue, float64(counts[kind][state]), kind, state)
		}
		age := float64(0)
		if at := oldest[kind]; !at.IsZero() {
			age = max(0, snapshot.Now.Sub(at).Seconds())
		}
		ch <- prometheus.MustNewConstMetric(c.age, prometheus.GaugeValue, age, kind)
	}
	if c.blocked != nil {
		ch <- prometheus.MustNewConstMetric(c.blocked, prometheus.GaugeValue, float64(snapshot.Blocked), "maxVersions")
	}
}

var tombstoneProbes = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "artifact_storage_tombstone_probes_total",
	Help: "Probes of known deleted file locations, by observed result; counts probes, not unique objects.",
}, []string{"result"})

func init() { prometheus.MustRegister(tombstoneProbes) }

// TombstoneProbe records only bounded outcomes, never object keys or file IDs.
func TombstoneProbe(result string) {
	switch result {
	case "clean", "object", "multipart", "error":
		tombstoneProbes.WithLabelValues(result).Inc()
	}
}
