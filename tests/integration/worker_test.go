package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func workerTask(t *testing.T, database *db.DB) table.BackgroundTask {
	t.Helper()
	now, err := table.DatabaseNow(database)
	if err != nil {
		t.Fatal(err)
	}
	row := table.BackgroundTask{
		ID:             uuid.NewString(),
		Kind:           "retention",
		TargetID:       uuid.NewString(),
		Payload:        []byte("{}"),
		RecoveryEvents: []byte("[]"),
		State:          "pending",
		NextRunAt:      now.Add(-time.Second),
		NotBefore:      now.Add(-time.Second),
		Revision:       1,
		RunGeneration:  1,
	}
	if err := database.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestWorkerLeaseFailureAndRecovery(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		fixture.HTTP(t, database, resolve)
		cfg := backgroundtask.DefaultConfig()
		cfg.MaxFailures = 2
		first, _ := backgroundtask.New(database, &cfg)
		second, _ := backgroundtask.New(database, &cfg)
		task := workerTask(t, database)
		ctx := context.Background()
		var wg sync.WaitGroup
		results := make(chan *table.BackgroundTask, 2)
		errs := make(chan error, 2)
		for _, worker := range []*backgroundtask.Worker{first, second} {
			wg.Add(1)
			go func() { defer wg.Done(); row, err := worker.Claim(ctx); results <- row; errs <- err }()
		}
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var claim *table.BackgroundTask
		count := 0
		for row := range results {
			if row != nil {
				claim = row
				count++
			}
		}
		if count != 1 {
			t.Fatalf("claims=%d", count)
		}
		if err := first.Renew(ctx, task.ID, claim.LeaseToken); err != nil {
			t.Fatal(err)
		}
		// Expiring the persisted lease models a dead process; takeover is not a failure.
		now, _ := table.DatabaseNow(database)
		past := now.Add(-time.Second)
		if err := database.Model(&table.BackgroundTask{}).
			Where("id = ?", task.ID).
			Update("lease_until", past).
			Error; err != nil {
			t.Fatal(err)
		}
		takeover, err := second.Claim(ctx)
		if err != nil || takeover == nil {
			t.Fatalf("takeover=%v err=%v", takeover, err)
		}
		if takeover.Attempts != 0 || takeover.TotalAttempts != 2 {
			t.Fatalf("crash consumed failure budget: %+v", takeover)
		}
		if err = first.Renew(ctx, task.ID, claim.LeaseToken); !errors.Is(err, backgroundtask.ErrLeaseLost) {
			t.Fatalf("old token renewed: %v", err)
		}
		if err = first.Fail(
			ctx,
			task.ID,
			claim.LeaseToken,
			errors.New("stale"),
		); !errors.Is(
			err,
			backgroundtask.ErrLeaseLost,
		) {
			t.Fatalf("old token failed: %v", err)
		}
		if err = second.Fail(ctx, task.ID, takeover.LeaseToken, errors.New("backend unavailable")); err != nil {
			t.Fatal(err)
		}
		if row, err := first.Claim(ctx); err != nil || row != nil {
			t.Fatalf("backoff not respected: %+v %v", row, err)
		}
		if err = database.Model(&table.BackgroundTask{}).
			Where("id = ?", task.ID).
			Update("next_run_at", past).
			Error; err != nil {
			t.Fatal(err)
		}
		claim, err = first.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatal(err)
		}
		if err = first.Fail(ctx, task.ID, claim.LeaseToken, errors.New("still unavailable")); err != nil {
			t.Fatal(err)
		}
		var failed table.BackgroundTask
		if err = database.First(&failed, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if failed.State != "failed" || failed.Attempts != 2 || failed.TotalAttempts != 3 {
			t.Fatalf("budget: %+v", failed)
		}
		if err = first.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if row, err := first.Claim(ctx); err != nil || row != nil {
			t.Fatalf("failed resurrected: %+v %v", row, err)
		}
		future := now.Add(time.Hour)
		if err = database.Model(&failed).Update("not_before", future).Error; err != nil {
			t.Fatal(err)
		}
		request := backgroundtask.RecoveryRequest{
			TaskID:           task.ID,
			ExpectedRevision: failed.Revision,
			Operator:         "operator",
			Reason:           "backend fixed",
			Record:           "OPS-1",
		}
		preview, err := first.Recover(ctx, request, false)
		if err != nil || preview.Affected != 1 {
			t.Fatalf("preview %v %v", preview, err)
		}
		if err = database.First(&failed, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if failed.State != "failed" {
			t.Fatal("dry-run changed task")
		}
		// Recovery preserves the stored deadline; MySQL DATETIME rounds input
		// to its declared precision before the recovery protocol reads it.
		originalNotBefore := failed.NotBefore
		wins := 0
		var mu sync.Mutex
		for _, worker := range []*backgroundtask.Worker{first, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := worker.Recover(ctx, request, true)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					wins++
				} else if !errors.Is(err, backgroundtask.ErrRecoveryConflict) {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("recovery winners=%d", wins)
		}
		if err = database.First(&failed, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		var audit []backgroundtask.RecoveryEvent
		if err = json.Unmarshal(failed.RecoveryEvents, &audit); err != nil {
			t.Fatal(err)
		}
		if failed.State != "pending" || failed.Attempts != 0 || failed.TotalAttempts != 3 ||
			failed.RunGeneration != 2 ||
			!failed.NotBefore.Equal(originalNotBefore) ||
			failed.NextRunAt.Before(originalNotBefore) ||
			len(audit) != 1 ||
			audit[0].PreviousFailures != 2 ||
			audit[0].PreviousError != "still unavailable" {
			t.Fatalf("recovery lost obligations: %+v audit=%+v", failed, audit)
		}
		if row, err := first.Claim(ctx); err != nil || row != nil {
			t.Fatalf("NotBefore ignored: %+v %v", row, err)
		}
	})
}

func TestWorkerRunCancellation(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		fixture.HTTP(t, database, resolve)
		worker, err := backgroundtask.New(database, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not stop")
		}
	})
}

func TestWorkerRetentionExecution(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		artifact := create(t, client, ctx, "worker-retention", "model")
		old := publishVersion(t, client, artifact.ID, "one")
		latest := publishVersion(t, client, artifact.ID, "two")
		policy, err := client.UpdateRetention(
			ctx,
			policyRequest(artifact.ID, 0, revision(1), api.OverflowDeleteOldest),
		)
		if err != nil {
			t.Fatal(err)
		}
		worker, _ := backgroundtask.New(database, nil)
		if err = worker.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		task, err := worker.Claim(ctx)
		if err != nil || task == nil {
			t.Fatalf("task %v err %v", task, err)
		}
		stale := *task
		stale.LeaseToken = "obsolete"
		if err = worker.Execute(ctx, &stale); !errors.Is(err, backgroundtask.ErrLeaseLost) {
			t.Fatalf("stale execute: %v", err)
		}
		if _, err = client.GetVersion(
			ctx,
			&api.GetVersionRequest{
				VersionPath: api.VersionPath{
					ArtifactPath: api.ArtifactPath{ArtifactID: artifact.ID},
					VersionID:    old.ID,
				},
			},
		); err != nil {
			t.Fatalf("stale token changed version: %v", err)
		}
		if err = worker.Execute(ctx, task); err != nil {
			t.Fatal(err)
		}
		_, err = client.GetVersion(
			ctx,
			&api.GetVersionRequest{
				VersionPath: api.VersionPath{
					ArtifactPath: api.ArtifactPath{ArtifactID: artifact.ID},
					VersionID:    old.ID,
				},
			},
		)
		expectError(t, err, 404, api.CodeResourceNotFound)
		list, err := client.ListVersions(
			ctx,
			&api.ListVersionsRequest{ArtifactPath: api.ArtifactPath{ArtifactID: artifact.ID}},
		)
		if err != nil || len(list.Items) != 1 || list.Items[0].ID != latest.ID {
			t.Fatalf("retention list %+v %v", list, err)
		}
		var row table.BackgroundTask
		if err = database.First(&row, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if row.State != "done" || row.Attempts != 0 {
			t.Fatalf("completion %+v", row)
		}
		// A due reevaluation reuses the task identity and starts a new generation.
		now, _ := table.DatabaseNow(database)
		if err = database.Model(&table.Artifact{}).
			Where("id = ?", artifact.ID).
			Update("next_retention_at", now.Add(-time.Second)).
			Error; err != nil {
			t.Fatal(err)
		}
		if err = worker.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		task, err = worker.Claim(ctx)
		if err != nil || task == nil || task.RunGeneration != 2 {
			t.Fatalf("reschedule %+v %v", task, err)
		}
		request := policyRequest(artifact.ID, policy.Revision, nil, api.OverflowDeleteOldest)
		request.Enabled = flag(false)
		if _, err = client.UpdateRetention(ctx, request); err != nil {
			t.Fatal(err)
		}
		if err = worker.Execute(ctx, task); err != nil {
			t.Fatal(err)
		}
		var owner table.Artifact
		if err = database.First(&owner, "id = ?", artifact.ID).Error; err != nil {
			t.Fatal(err)
		}
		if owner.NextRetentionAt != nil {
			t.Fatal("disabled policy remained scheduled")
		}
	})
}

func TestRecoveryUnknownCommitOutcome(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		fixture.HTTP(t, database, resolve)
		worker, _ := backgroundtask.New(database, nil)
		conn, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		for _, persist := range []bool{true, false} {
			task := workerTask(t, database)
			if err = database.Model(&task).
				Updates(map[string]any{"state": "failed", "attempts": 20}).
				Error; err != nil {
				t.Fatal(err)
			}
			request := backgroundtask.RecoveryRequest{
				TaskID:           task.ID,
				ExpectedRevision: task.Revision,
				Operator:         "ops",
				Reason:           "fixed",
				Record:           "OPS-3",
			}
			pool := &uncertainCommitPool{ConnPool: conn, connection: conn, persist: persist}
			database.Statement.ConnPool = pool
			_, err = worker.Recover(context.Background(), request, true)
			database.Statement.ConnPool = original
			if persist && err != nil {
				t.Fatalf("committed audit not recognized: %v", err)
			}
			if !persist && (err == nil || !strings.Contains(err.Error(), "OperationOutcomeUnknown")) {
				t.Fatalf("ambiguous commit: %v", err)
			}
			if pool.commits.Load() != 1 {
				t.Fatal("uncertain recovery retried")
			}
			var row table.BackgroundTask
			if err = database.First(&row, "id = ?", task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if persist {
				if row.State != "pending" || row.RunGeneration != 2 {
					t.Fatalf("recovery missing %+v", row)
				}
			} else if row.State != "failed" || row.RunGeneration != 1 {
				t.Fatalf("rollback lost obligation %+v", row)
			}
		}
	})
}

func TestWorkerRenewsDuringRetention(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		artifact := create(t, client, ctx, "renew-retention", "model")
		publishVersion(t, client, artifact.ID, "one")
		publishVersion(t, client, artifact.ID, "two")
		if _, err := client.UpdateRetention(
			ctx,
			policyRequest(artifact.ID, 0, revision(1), api.OverflowDeleteOldest),
		); err != nil {
			t.Fatal(err)
		}
		worker, _ := backgroundtask.New(database, nil)
		if err := worker.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		task, err := worker.Claim(ctx)
		if err != nil || task == nil {
			t.Fatalf("claim %+v %v", task, err)
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		callbackName := "test:retention-update-barrier"
		if err = database.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == "versions" {
				once.Do(func() { close(entered) })
				<-release
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer database.Callback().Update().Remove(callbackName)
		done := make(chan error, 1)
		go func() { done <- worker.Execute(ctx, task) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("retention update not reached")
		}
		renewCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = worker.Renew(renewCtx, task.ID, task.LeaseToken)
		cancel()
		close(release)
		if err != nil {
			t.Errorf("renewal blocked by retention: %v", err)
		}
		select {
		case err = <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("retention did not finish")
		}
	})
}

func TestRetentionUnknownCommitPreservesFailureBudget(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		worker, _ := backgroundtask.New(database, nil)
		conn, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		for _, persist := range []bool{true, false} {
			name := "unknown-retention-rollback"
			if persist {
				name = "unknown-retention-commit"
			}
			artifact := create(t, client, ctx, name, "model")
			publishVersion(t, client, artifact.ID, "one")
			publishVersion(t, client, artifact.ID, "two")
			if _, err = client.UpdateRetention(
				ctx,
				policyRequest(artifact.ID, 0, revision(1), api.OverflowDeleteOldest),
			); err != nil {
				t.Fatal(err)
			}
			if err = worker.Scan(ctx); err != nil {
				t.Fatal(err)
			}
			task, err := worker.Claim(ctx)
			if err != nil || task == nil {
				t.Fatalf("claim %+v %v", task, err)
			}
			pool := &uncertainCommitPool{ConnPool: conn, connection: conn, persist: persist}
			database.Statement.ConnPool = pool
			err = worker.Execute(ctx, task)
			database.Statement.ConnPool = original
			if !errors.Is(err, backgroundtask.ErrOutcomeUnknown) {
				t.Fatalf("unknown outcome not recognized: %v", err)
			}
			if pool.commits.Load() != 1 {
				t.Fatal("uncertain execution retried")
			}
			var row table.BackgroundTask
			if err = database.First(&row, "id = ?", task.ID).Error; err != nil {
				t.Fatal(err)
			}
			state := "running"
			if persist {
				state = "done"
			}
			if row.State != state || row.Attempts != 0 || row.TotalAttempts != 1 {
				t.Fatalf("unknown completion consumed failure budget %+v", row)
			}
		}
	})
}
