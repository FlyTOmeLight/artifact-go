package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	storageapi "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func observedHTTP(t *testing.T, database *db.DB, enabled bool) (*httptest.Server, string) {
	t.Helper()
	svc, err := service.New(t.Context(), database, service.Options{IdentityResolver: resolve, AutoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &conf.Config{AppName: "observability-test", AppCode: 100, RunMode: conf.RunModeRelease}
	cfg.HTTPServer.MetricsEnable = enabled
	manager, err := conf.NewManagerFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	path := filepath.Join(t.TempDir(), "request.log")
	log, err := logger.NewLogger(
		&logger.Config{Level: "info", Outputs: []logger.Output{{Type: "file", Path: path}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	h, err := host.NewHTTPHost(manager, log, service.SetupHTTP, svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	server := httptest.NewServer(h.Handler())
	t.Cleanup(server.Close)
	return server, path
}

func requestMetric(t *testing.T, domain, method, route, status string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "artifact_service_requests_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["domain"] == domain && labels["method"] == method && labels["route"] == route &&
				labels["status"] == status {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestBusinessRequestObservability(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server, path := observedHTTP(t, database, true)
		client := versionClient(t, server.URL)
		a := create(t, client, identity(t, "tenant"), "observed", "config")
		route := "/v1/artifacts/:artifactID/versions"
		beforeCreated := requestMetric(t, "artifact", "POST", route, "201")
		beforeConflict := requestMetric(t, "artifact", "POST", route, "409")
		beforeUnauthorized := requestMetric(t, "artifact", "POST", route, "401")
		v := published(
			t,
			publishRaw(t, server.URL, a.ID, "tenant", `{"requestKey":"observed","payload":{"a":1}}`, 201),
		)
		publishRaw(t, server.URL, a.ID, "tenant", `{"requestKey":"observed","payload":{"a":2}}`, 409)
		secret := "never-log-this-credential"
		publishRaw(t, server.URL, a.ID, secret, `{"requestKey":"bad-auth","payload":{}}`, 401)
		for _, check := range []struct {
			status string
			before float64
		}{{status: "201", before: beforeCreated}, {status: "409", before: beforeConflict}, {status: "401", before: beforeUnauthorized}} {
			if got := requestMetric(t, "artifact", "POST", route, check.status); got != check.before+1 {
				t.Fatalf("status=%s count=%g before=%g", check.status, got, check.before)
			}
		}
		response, err := http.Get(server.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || !strings.Contains(string(raw), "artifact_lock_acquire_seconds_count") {
			t.Fatal("framework metrics endpoint missing business lock observations")
		}
		for _, sensitive := range []string{secret, tenantA, a.ID, v.ID} {
			if strings.Contains(string(raw), sensitive) {
				t.Fatalf("resource identity or credential leaked to metric labels: %s", sensitive)
			}
		}
		logs, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(logs), secret) {
			t.Fatal("credential leaked into log")
		}
		if !strings.Contains(string(logs), "event=business_request") || !strings.Contains(string(logs), a.ID) ||
			!strings.Contains(string(logs), v.ID) ||
			!strings.Contains(string(logs), tenantA) {
			t.Fatal("missing correlated resource log")
		}
		disabled, _ := observedHTTP(t, database, false)
		response, err = http.Get(disabled.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Fatal("metrics endpoint must remain opt-in")
		}
	})
}

func TestArtifactLockMetricIncludesRealContention(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server, _ := observedHTTP(t, database, true)
		client := versionClient(t, server.URL)
		a := create(t, client, identity(t, "tenant"), "lock-observed", "config")
		histogram := func() (uint64, float64) {
			families, err := prometheus.DefaultGatherer.Gather()
			if err != nil {
				t.Fatal(err)
			}
			for _, family := range families {
				if family.GetName() == "artifact_lock_acquire_seconds" {
					for _, m := range family.Metric {
						for _, label := range m.Label {
							if label.GetName() == "operation" &&
								label.GetValue() == "version" {
								return m.GetHistogram().
										GetSampleCount(),
									m.GetHistogram().
										GetSampleSum()
							}
						}
					}
				}
			}
			return 0, 0
		}
		beforeCount, beforeSum := histogram()
		lock := database.Begin()
		if lock.Error != nil {
			t.Fatal(lock.Error)
		}
		defer lock.Rollback()
		var row table.Artifact
		if err := lock.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&row, "id = ?", a.ID).
			Error; err != nil {
			t.Fatal(err)
		}
		var connection int64
		query := "SELECT pg_backend_pid()"
		waits := "SELECT COUNT(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid))"
		if database.Dialector.Name() == "mysql" {
			query = "SELECT CONNECTION_ID()"
			waits = "SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.threads t ON t.THREAD_ID = w.BLOCKING_THREAD_ID WHERE t.PROCESSLIST_ID = ?"
		}
		if err := lock.Raw(query).Scan(&connection).Error; err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(identity(t, "tenant"), 10*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := client.CreateVersion(
				ctx,
				&api.CreateVersionRequest{
					ArtifactID: a.ID,
					RequestKey: "wait",
					Payload:    json.RawMessage(`{}`),
				},
			)
			done <- err
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var n int64
			if err := database.Raw(waits, connection).Scan(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("request did not reach a database lock wait")
			}
			select {
			case err := <-done:
				t.Fatalf("request escaped row lock: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err := lock.Rollback().Error; err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		count, sum := histogram()
		if count <= beforeCount || sum <= beforeSum {
			t.Fatal("actual contended request did not update lock histogram")
		}
	})
}

func TestBatchFileErrorsRemainObservableUnderHTTP200(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		client, _, backend := storageFileHTTP(t, database, nil)
		count := func(operation, result string) float64 {
			families, err := prometheus.DefaultGatherer.Gather()
			if err != nil {
				t.Fatal(err)
			}
			for _, family := range families {
				if family.GetName() == "artifact_storage_file_results_total" {
					for _, m := range family.Metric {
						labels := map[string]string{}
						for _, l := range m.Label {
							labels[l.GetName()] = l.GetValue()
						}
						if labels["operation"] == operation && labels["result"] == result {
							return m.GetCounter().GetValue()
						}
					}
				}
			}
			return 0
		}
		okBefore, badBefore := count("register", "success"), count("register", "failure")
		good, bad := int64(1), int64(-1)
		results, err := client.RegisterFiles(
			storageIdentity(t, "tenant"),
			storageapi.RegisterFilesRequest{
				{RequestKey: "good", Size: &good},
				{RequestKey: "bad", Size: &bad},
			},
		)
		if err != nil || len(results) != 2 || results[0].Error != nil || results[1].Error == nil {
			t.Fatalf("partial registration: %+v %v", results, err)
		}
		if count("register", "success") != okBefore+1 || count("register", "failure") != badBefore+1 {
			t.Fatal("HTTP 200 hid per-item failures")
		}
		before := count("complete", "failure")
		backend.objectStatus.Store(404)
		complete, err := client.CompleteFiles(
			storageIdentity(t, "tenant"),
			storageapi.CompleteFilesRequest{results[0].FileID},
		)
		if err != nil || len(complete) != 1 || complete[0].Error == nil {
			t.Fatalf("confirmation: %+v %v", complete, err)
		}
		if count("complete", "failure") != before+1 {
			t.Fatal("missing failed confirmation metric")
		}
	})
}
