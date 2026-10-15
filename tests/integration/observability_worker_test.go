package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func taskMetric(t *testing.T, name, domain, kind, state string) (float64, int) {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	count := 0
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["domain"] != domain || (kind != "" && labels["kind"] != kind) ||
				(state != "" && labels["state"] != state) {
				continue
			}
			if metric.Gauge != nil {
				sum += metric.Gauge.GetValue()
			} else if metric.Counter != nil {
				sum += metric.Counter.GetValue()
			}
			count++
		}
	}
	return sum, count
}

func TestWorkerMetricsObserveBothDomainsAndUnregister(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, artifactDB *db.DB) {
		if err := table.Migrate(t.Context(), artifactDB); err != nil {
			t.Fatal(err)
		}
		driver := db.DriverPostgreSQL
		if artifactDB.Dialector.Name() == "mysql" {
			driver = db.DriverMySQL
		}
		storageDB := fixture.Open(t, driver)
		if err := st.Migrate(t.Context(), storageDB); err != nil {
			t.Fatal(err)
		}
		now, err := table.DatabaseNow(artifactDB)
		if err != nil {
			t.Fatal(err)
		}
		a := table.Artifact{
			ID:             uuid.NewString(),
			TenantID:       "tenant",
			ScopeKey:       "tenant",
			Type:           "model",
			Name:           "metrics",
			VersionScheme:  "integer",
			InitialVersion: "1",
			State:          "active",
			CreatedBy:      "test",
		}
		if err := artifactDB.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		if err := artifactDB.Create(
			&table.RetentionPolicy{
				ArtifactID:     a.ID,
				Enabled:        true,
				OverflowAction: "deleteOldest",
				Revision:       1,
				LastResult:     []byte(`["maxVersions"]`),
			},
		).Error; err != nil {
			t.Fatal(err)
		}
		at := table.BackgroundTask{
			ID:             uuid.NewString(),
			Kind:           "reference-release",
			TargetID:       uuid.NewString(),
			Payload:        []byte(`{}`),
			State:          "pending",
			NextRunAt:      now.Add(-time.Minute),
			NotBefore:      now.Add(-time.Minute),
			Revision:       1,
			RunGeneration:  1,
			RecoveryEvents: []byte(`[]`),
		}
		sf := st.StorageTask{
			ID:             uuid.NewString(),
			Kind:           "delete",
			TargetID:       uuid.NewString(),
			Payload:        []byte(`{}`),
			State:          "failed",
			NextRunAt:      now,
			NotBefore:      now,
			Revision:       1,
			RunGeneration:  1,
			RecoveryEvents: []byte(`[]`),
		}
		if err := artifactDB.Create(&at).Error; err != nil {
			t.Fatal(err)
		}
		if err := storageDB.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		// Keep the due task claim-locked: collection must use ordinary reads rather
		// than changing task ownership or waiting for this unrelated writer.
		locked := artifactDB.Begin()
		if locked.Error != nil {
			t.Fatal(locked.Error)
		}
		defer locked.Rollback()
		var row table.BackgroundTask
		if err := locked.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&row, "id = ?", at.ID).
			Error; err != nil {
			t.Fatal(err)
		}
		ac := backgroundtask.DefaultConfig()
		ac.Concurrency = 1
		ac.ScanInterval = time.Hour
		aw, _ := backgroundtask.New(artifactDB, &ac)
		sc := storage.DefaultWorkerConfig()
		sc.Concurrency = 1
		sc.ScanInterval = time.Hour
		sw, _ := storage.NewWorker(storageDB, &gcObjects{}, &sc)
		secondDB := fixture.Open(t, driver)
		if err := table.Migrate(t.Context(), secondDB); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			failed := at
			failed.ID = uuid.NewString()
			failed.TargetID = uuid.NewString()
			failed.State = "failed"
			if err := secondDB.Create(&failed).Error; err != nil {
				t.Fatal(err)
			}
		}
		secondWorker, _ := backgroundtask.New(secondDB, &ac)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 3)
		go func() { done <- aw.Run(ctx) }()
		go func() { done <- sw.Run(ctx) }()
		go func() { done <- secondWorker.Run(ctx) }()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			_, n := taskMetric(t, "artifact_background_task_collection_success", "artifact", "", "")
			_, m := taskMetric(t, "artifact_background_task_collection_success", "storage", "", "")
			if n == 2 && m == 1 {
				break
			}
			select {
			case <-deadline.C:
				t.Fatal("worker collectors not registered")
			case <-tick.C:
			}
		}
		for _, tc := range []struct {
			name, domain, kind, state string
			want                      float64
		}{
			{name: "artifact_background_tasks", domain: "artifact", kind: "reference-release", state: "pending", want: 1},
			{name: "artifact_background_tasks", domain: "storage", kind: "delete", state: "failed", want: 1},
			{name: "artifact_retention_blocked_artifacts", domain: "artifact", kind: "", state: "", want: 1},
			{name: "artifact_background_task_collection_success", domain: "artifact", kind: "", state: "", want: 2},
			{name: "artifact_background_tasks", domain: "artifact", kind: "reference-release", state: "failed", want: 2},
			{name: "artifact_background_task_collection_success", domain: "storage", kind: "", state: "", want: 1},
		} {
			if got, _ := taskMetric(t, tc.name, tc.domain, tc.kind, tc.state); got != tc.want {
				t.Fatalf("%s/%s/%s/%s=%v want %v", tc.name, tc.domain, tc.kind, tc.state, got, tc.want)
			}
		}
		if age, _ := taskMetric(
			t,
			"artifact_background_task_oldest_due_age_seconds",
			"artifact",
			"reference-release",
			"",
		); age < 59 {
			t.Fatalf("due age %v", age)
		}
		// Count only confirmed failure writes, not rejected stale leases.
		running := sf
		running.ID = uuid.NewString()
		running.TargetID = uuid.NewString()
		running.State = "running"
		running.LeaseToken = uuid.NewString()
		until := now.Add(time.Hour)
		running.LeaseUntil = &until
		if err := storageDB.Create(&running).Error; err != nil {
			t.Fatal(err)
		}
		before, _ := taskMetric(t, "artifact_service_task_failures_total", "storage", "delete", "")
		if err := sw.Fail(
			t.Context(),
			running.ID,
			running.LeaseToken,
			errors.New("controlled backend failure"),
		); err != nil {
			t.Fatal(err)
		}
		if err := sw.Fail(
			t.Context(),
			running.ID,
			running.LeaseToken,
			errors.New("stale"),
		); !errors.Is(
			err,
			storage.ErrLeaseLost,
		) {
			t.Fatalf("stale lease: %v", err)
		}
		after, _ := taskMetric(t, "artifact_service_task_failures_total", "storage", "delete", "")
		if after != before+1 {
			t.Fatalf("failure counter %v -> %v", before, after)
		}
		cancel()
		for range 3 {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker stop timeout")
			}
		}
		for _, domain := range []string{"artifact", "storage"} {
			if _, n := taskMetric(
				t,
				"artifact_background_task_collection_success",
				domain,
				"",
				"",
			); n != 0 {
				t.Fatal("worker collector remained registered")
			}
		}
	})
}

type unavailableMetricsRegistry struct{}

func (unavailableMetricsRegistry) Register(prometheus.Collector) error {
	return errors.New("controlled metrics registry unavailable")
}

func (unavailableMetricsRegistry) MustRegister(...prometheus.Collector) { panic("unused") }

func (unavailableMetricsRegistry) Unregister(prometheus.Collector) bool { return false }

func TestMetricsRegistrationFailureDoesNotStopWorkers(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		if err := table.Migrate(t.Context(), database); err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(t.Context(), database); err != nil {
			t.Fatal(err)
		}
		a := table.Artifact{
			ID:             uuid.NewString(),
			TenantID:       "tenant",
			ScopeKey:       "tenant",
			Type:           "model",
			Name:           "metrics-failure",
			VersionScheme:  "integer",
			InitialVersion: "1",
			State:          "active",
			CreatedBy:      "test",
		}
		if err := database.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		task := workerTask(t, database)
		if err := database.Model(&task).Update("target_id", a.ID).Error; err != nil {
			t.Fatal(err)
		}
		file := gcFile(t, database, "uploading")
		if err := db.Transaction(
			t.Context(),
			database,
			db.TransactionOptions{},
			func(tx *db.DB) error { return storage.MarkDeleting(tx, &file) },
		); err != nil {
			t.Fatal(err)
		}
		ac := backgroundtask.DefaultConfig()
		ac.Concurrency = 1
		ac.ScanInterval = 10 * time.Millisecond
		aw, _ := backgroundtask.New(database, &ac)
		sc := storage.DefaultWorkerConfig()
		sc.Concurrency = 1
		sc.ScanInterval = 10 * time.Millisecond
		sw, _ := storage.NewWorker(database, &gcObjects{}, &sc)
		original := prometheus.DefaultRegisterer
		prometheus.DefaultRegisterer = unavailableMetricsRegistry{}
		defer func() { prometheus.DefaultRegisterer = original }()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 2)
		go func() { done <- aw.Run(ctx) }()
		go func() { done <- sw.Run(ctx) }()
		defer func() {
			cancel()
			for range 2 {
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(3 * time.Second):
					t.Error("worker did not stop")
				}
			}
		}()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var current table.BackgroundTask
			var currentFile st.StorageFile
			if err := database.First(&current, "id = ?", task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.First(&currentFile, "id = ?", file.ID).Error; err != nil {
				t.Fatal(err)
			}
			if current.State == "done" && currentFile.State == "deleted" {
				break
			}
			select {
			case <-deadline.C:
				t.Fatalf(
					"registration error stopped business work: artifact=%s storage=%s",
					current.State,
					currentFile.State,
				)
			case <-ticker.C:
			}
		}
	})
}

func tombstoneProbeCount(t *testing.T, result string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "artifact_storage_tombstone_probes_total" {
			continue
		}
		for _, m := range family.Metric {
			for _, label := range m.Label {
				if label.GetName() == "result" && label.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestTombstoneProbeMetricsDistinguishObservedResults(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		for _, tc := range []struct {
			name, result                                          string
			object, parts, statError, partsError, backendMismatch bool
		}{
			{name: "clean", result: "clean"},
			{name: "late object", result: "object", object: true},
			{name: "late multipart", result: "multipart", parts: true},
			{name: "stat error", result: "error", statError: true},
			{name: "multipart probe error", result: "error", partsError: true},
			{name: "backend mismatch", result: "error", backendMismatch: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				objects := &gcObjects{}
				w := gcWorker(t, database, objects)
				file := gcFile(t, database, "uploading")
				if err := w.Scan(t.Context()); err != nil {
					t.Fatal(err)
				}
				task, err := w.Claim(t.Context())
				if err != nil || task == nil {
					t.Fatalf("claim %v %v", task, err)
				}
				if err := w.Execute(t.Context(), task); err != nil {
					t.Fatal(err)
				}
				objects.exists, objects.parts, objects.statFail, objects.partsFail = tc.object, tc.parts, tc.statError, tc.partsError
				updates := map[string]any{"next_sweep_at": time.Now().Add(-time.Hour)}
				if tc.backendMismatch {
					updates["backend_id"] = "unconfigured"
				}
				if err := database.Model(&file).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				before := tombstoneProbeCount(t, tc.result)
				if err := w.Scan(t.Context()); err != nil {
					t.Fatal(err)
				}
				if after := tombstoneProbeCount(t, tc.result); after != before+1 {
					t.Fatalf("%s probe count %v -> %v", tc.result, before, after)
				}
				// Do not leave this subtest's deletion job available to the next case.
				if err := database.Model(&st.StorageTask{}).
					Where("target_id = ?", file.ID).
					Update("state", "failed").
					Error; err != nil {
					t.Fatal(err)
				}
			})
		}
	})
}
