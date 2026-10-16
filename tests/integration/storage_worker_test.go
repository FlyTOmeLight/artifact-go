package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	client "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

type gcObjects struct {
	mu        sync.Mutex
	exists    bool
	parts     bool
	calls     int
	fail      bool
	statFail  bool
	partsFail bool
	entered   chan struct{}
	release   chan struct{}
}

func (o *gcObjects) BackendID() string { return "local" }

func (o *gcObjects) HasIncompleteUploads(context.Context, string, string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.partsFail {
		return false, errors.New("multipart probe failed")
	}
	return o.parts, nil
}

func (o *gcObjects) Stat(context.Context, string, string) (client.ObjectInfo, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.statFail {
		return client.ObjectInfo{}, errors.New("stat unavailable")
	}
	if !o.exists {
		return client.ObjectInfo{}, client.ErrObjectNotFound
	}
	return client.ObjectInfo{}, nil
}

func (o *gcObjects) Delete(ctx context.Context, _, _ string) error {
	if o.entered != nil {
		select {
		case o.entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-o.release:
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.fail {
		return errors.New("backend unavailable")
	}
	o.exists = false
	o.parts = false
	return nil
}

func gcFile(t *testing.T, database *db.DB, state string) st.StorageFile {
	t.Helper()
	now, err := st.DatabaseNow(database)
	if err != nil {
		t.Fatal(err)
	}
	row := st.StorageFile{
		ID:          uuid.NewString(),
		TenantID:    "t_000000000000000000000001",
		ScopeKey:    "tenant",
		CreatedBy:   "user",
		RequestKey:  uuid.NewString(),
		RequestHash: "hash",
		BackendID:   "local",
		Bucket:      "tenant-test",
		ObjectKey:   uuid.NewString(),
		State:       state,
		ExpiresAt:   now.Add(-time.Hour),
	}
	if err = database.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func gcWorker(t *testing.T, database *db.DB, objects *gcObjects) *storage.Worker {
	t.Helper()
	if err := st.Migrate(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	cfg := storage.DefaultWorkerConfig()
	cfg.MaxFailures = 1
	cfg.LeaseDuration = time.Second
	cfg.RenewInterval = 50 * time.Millisecond
	cfg.ScanInterval = 20 * time.Millisecond
	w, err := storage.NewWorker(database, objects, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestStorageWorkerExpiryLateWriteAndFailedRecovery(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		ctx := context.Background()
		objects := &gcObjects{exists: true}
		w := gcWorker(t, database, objects)
		file := gcFile(t, database, "ready")
		bound := gcFile(t, database, "bound")
		if err := w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		claim, err := w.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatalf("claim %v %v", claim, err)
		}
		if err = w.Execute(ctx, claim); err != nil {
			t.Fatal(err)
		}
		if err = database.First(
			&file,
			"id = ?",
			file.ID,
		).Error; err != nil || file.State != "deleted" ||
			file.DeletedAt == nil {
			t.Fatalf("terminal %+v %v", file, err)
		}
		if err = database.First(&bound, "id = ?", bound.ID).Error; err != nil || bound.State != "bound" {
			t.Fatalf("bound expired %+v %v", bound, err)
		}
		objects.mu.Lock()
		objects.exists = true
		objects.fail = true
		objects.mu.Unlock()
		if err = database.Model(&file).Update("next_sweep_at", time.Now().Add(-time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
		if err = w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		claim, err = w.Claim(ctx)
		if err != nil || claim == nil || claim.RunGeneration != 2 {
			t.Fatalf("late write claim %v %v", claim, err)
		}
		err = w.Execute(ctx, claim)
		if err == nil {
			t.Fatal("expected backend failure")
		}
		if err = w.Fail(ctx, claim.ID, claim.LeaseToken, err); err != nil {
			t.Fatal(err)
		}
		if err = w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		none, err := w.Claim(ctx)
		if err != nil || none != nil {
			t.Fatalf("revived failed task %v %v", none, err)
		}
		var task st.StorageTask
		if err = database.First(&task, "id = ?", claim.ID).Error; err != nil {
			t.Fatal(err)
		}
		req := storage.RecoveryRequest{
			TaskID:           task.ID,
			ExpectedRevision: task.Revision,
			Operator:         "operator",
			Reason:           "backend repaired",
			Record:           "t07",
		}
		preview, err := storage.RecoverTask(ctx, database, req, false)
		if err != nil || preview.Attempts != 1 {
			t.Fatalf("preview %v %v", preview, err)
		}
		if _, err = storage.RecoverTask(ctx, database, req, true); err != nil {
			t.Fatal(err)
		}
		if _, err = storage.RecoverTask(
			ctx,
			database,
			req,
			true,
		); !errors.Is(
			err,
			storage.ErrRecoveryConflict,
		) {
			t.Fatalf("recovery replay %v", err)
		}
		objects.mu.Lock()
		objects.fail = false
		objects.mu.Unlock()
		claim, err = w.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatalf("recovery claim %v %v", claim, err)
		}
		if err = w.Execute(ctx, claim); err != nil {
			t.Fatal(err)
		}
		if err = database.First(
			&task,
			"id = ?",
			claim.ID,
		).Error; err != nil || task.State != "done" || task.LastError == "" ||
			len(task.RecoveryEvents) < 10 {
			t.Fatalf("audit %+v %v", task, err)
		}
	})
}

func TestStorageWorkerGrantWaitAndStaleToken(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		ctx := context.Background()
		objects := &gcObjects{exists: true}
		w := gcWorker(t, database, objects)
		file := gcFile(t, database, "uploading")
		future := time.Now().Add(time.Hour)
		if err := database.Model(&file).Update("last_grant_expires_at", future).Error; err != nil {
			t.Fatal(err)
		}
		if err := w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		claim, err := w.Claim(ctx)
		if err != nil || claim != nil {
			t.Fatalf("claimed before grants expire %v %v", claim, err)
		}
		past := time.Now().Add(-time.Hour)
		if err = database.Model(&file).Update("last_grant_expires_at", past).Error; err != nil {
			t.Fatal(err)
		}
		if err = database.Model(&st.StorageTask{}).
			Where("target_id = ?", file.ID).
			Updates(map[string]any{"not_before": past, "next_run_at": past}).
			Error; err != nil {
			t.Fatal(err)
		}
		claim, err = w.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatalf("claim %v %v", claim, err)
		}
		if err = database.Model(&st.StorageTask{}).
			Where("id = ?", claim.ID).
			Update("lease_until", past).
			Error; err != nil {
			t.Fatal(err)
		}
		newer, err := w.Claim(ctx)
		if err != nil || newer == nil || newer.Attempts != 0 {
			t.Fatalf("takeover %v %v", newer, err)
		}
		if err = w.Execute(ctx, claim); !errors.Is(err, storage.ErrLeaseLost) {
			t.Fatalf("stale execute %v", err)
		}
		if objects.calls != 0 {
			t.Fatal("stale worker performed I/O")
		}
		if err = w.Execute(ctx, newer); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStorageWorkerRenewsDuringObjectIOAndStops(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		objects := &gcObjects{exists: true, entered: make(chan struct{}, 1), release: make(chan struct{})}
		w := gcWorker(t, database, objects)
		file := gcFile(t, database, "ready")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()
		select {
		case <-objects.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("delete not started")
		}
		var first st.StorageTask
		if err := database.Where("target_id = ?", file.ID).First(&first).Error; err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		renewed := false
		for time.Now().Before(deadline) {
			var current st.StorageTask
			if err := database.First(&current, "id = ?", first.ID).Error; err != nil {
				t.Fatal(err)
			}
			if current.Revision > first.Revision && current.LeaseUntil.After(*first.LeaseUntil) {
				renewed = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !renewed {
			t.Fatal("I/O blocked lease renewals")
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not stop")
		}
	})
}

func TestStorageWorkerFencesAfterObjectIO(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		objects := &gcObjects{exists: true, entered: make(chan struct{}, 1), release: make(chan struct{})}
		w := gcWorker(t, database, objects)
		file := gcFile(t, database, "ready")
		ctx := context.Background()
		if err := w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		claim, err := w.Claim(ctx)
		if err != nil || claim == nil {
			t.Fatalf("claim %v %v", claim, err)
		}
		done := make(chan error, 1)
		go func() { done <- w.Execute(ctx, claim) }()
		select {
		case <-objects.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("object I/O not reached")
		}
		if err = database.Model(&st.StorageTask{}).
			Where("id = ?", claim.ID).
			Update("lease_until", time.Now().Add(-time.Hour)).
			Error; err != nil {
			t.Fatal(err)
		}
		takeover, err := w.Claim(ctx)
		if err != nil || takeover == nil {
			t.Fatalf("takeover %v %v", takeover, err)
		}
		close(objects.release)
		if err = <-done; !errors.Is(err, storage.ErrLeaseLost) {
			t.Fatalf("stale completion %v", err)
		}
		if err = database.First(&file, "id = ?", file.ID).Error; err != nil || file.State != "deleting" {
			t.Fatalf("stale worker committed %+v %v", file, err)
		}
		if err = w.Execute(ctx, takeover); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStorageWorkerFindsLateMultipartWithoutObject(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		objects := &gcObjects{}
		w := gcWorker(t, database, objects)
		file := gcFile(t, database, "uploading")
		ctx := context.Background()
		if err := w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		task, err := w.Claim(ctx)
		if err != nil || task == nil {
			t.Fatalf("claim %v %v", task, err)
		}
		if err = w.Execute(ctx, task); err != nil {
			t.Fatal(err)
		}
		objects.mu.Lock()
		objects.parts = true
		objects.mu.Unlock()
		if err = database.Model(&file).Update("next_sweep_at", time.Now().Add(-time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
		if err = w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		task, err = w.Claim(ctx)
		if err != nil || task == nil || task.RunGeneration != 2 {
			t.Fatalf("multipart claim %v %v", task, err)
		}
		if err = w.Execute(ctx, task); err != nil {
			t.Fatal(err)
		}
		objects.mu.Lock()
		defer objects.mu.Unlock()
		if objects.parts {
			t.Fatal("late multipart retained")
		}
	})
}

func TestStorageWorkerProbeFailuresUseBudgetWithoutBlockingTombstones(t *testing.T) {
	for _, multipart := range []bool{false, true} {
		t.Run(fmt.Sprint(multipart), func(t *testing.T) {
			fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
				objects := &gcObjects{}
				w := gcWorker(t, database, objects)
				ctx := context.Background()
				for range 2 {
					gcFile(t, database, "ready")
				}
				if err := w.Scan(ctx); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					task, err := w.Claim(ctx)
					if err != nil || task == nil {
						t.Fatalf("claim %v %v", task, err)
					}
					if err = w.Execute(ctx, task); err != nil {
						t.Fatal(err)
					}
				}
				objects.mu.Lock()
				objects.statFail = !multipart
				objects.partsFail = multipart
				objects.fail = true
				objects.mu.Unlock()
				if err := database.Model(&st.StorageFile{}).
					Where("state = ?", "deleted").
					Update("next_sweep_at", time.Now().Add(-time.Hour)).
					Error; err != nil {
					t.Fatal(err)
				}
				if err := w.Scan(ctx); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					task, err := w.Claim(ctx)
					if err != nil || task == nil {
						t.Fatalf("poison tombstone blocked later work %v %v", task, err)
					}
					cause := w.Execute(ctx, task)
					if cause == nil {
						t.Fatal("expected delete failure")
					}
					if err = w.Fail(ctx, task.ID, task.LeaseToken, cause); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Scan(ctx); err != nil {
					t.Fatal(err)
				}
				task, err := w.Claim(ctx)
				if err != nil || task != nil {
					t.Fatalf("unbounded probe retry %v %v", task, err)
				}
			})
		})
	}
}
