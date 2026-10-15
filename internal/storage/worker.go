package storage

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/storage/client"
	"github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
)

// ObjectDeleter performs object I/O outside storage database transactions.
type ObjectDeleter interface {
	BackendID() string
	Stat(context.Context, string, string) (client.ObjectInfo, error)
	Delete(context.Context, string, string) error
	HasIncompleteUploads(context.Context, string, string) (bool, error)
}

// MarkDeleting requires the caller to hold the file row lock in this transaction.
func MarkDeleting(tx *db.DB, file *table.StorageFile) error {
	if file.State == "bound" {
		count, err := activeReferences(tx, file.ID)
		if err != nil {
			return err
		}
		if count != 0 {
			return errors.New("bound file requires reference release")
		}
	}
	if file.State == "deleted" || file.State == "deleting" {
		return nil
	}
	now, err := table.DatabaseNow(tx)
	if err != nil {
		return err
	}
	file.State = "deleting"
	if err = tx.Save(file).Error; err != nil {
		return err
	}
	notBefore := now
	if file.LastGrantExpiresAt != nil && notBefore.Before(*file.LastGrantExpiresAt) {
		notBefore = *file.LastGrantExpiresAt
	}
	task := table.StorageTask{
		ID:             uuid.NewString(),
		Kind:           "delete",
		TargetID:       file.ID,
		Payload:        []byte("{}"),
		State:          "pending",
		NextRunAt:      notBefore,
		NotBefore:      notBefore,
		Revision:       1,
		RunGeneration:  1,
		RecoveryEvents: []byte("[]"),
	}
	return tx.Create(&task).Error
}

// Scan atomically expires unbound preparations and schedules their deletion.
func (w *Worker) Scan(ctx context.Context) error {
	if err := workerTransaction(ctx, w.database, func(tx *db.DB) error {
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		var files []table.StorageFile
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("state IN ? AND expires_at <= ?", []string{"uploading", "ready"}, now).
			Order("id").
			Limit(100).
			Find(&files).
			Error; err != nil {
			return err
		}
		for i := range files {
			if err = MarkDeleting(tx, &files[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return w.sweep(ctx)
}

// sweep checks immutable terminal locations; it never revives failed tasks or files.
func (w *Worker) sweep(ctx context.Context) error {
	now, err := table.DatabaseNow(w.database.WithContext(ctx))
	if err != nil {
		return err
	}
	var files []table.StorageFile
	if err = w.database.WithContext(ctx).
		Where("state = ? AND next_sweep_at <= ? AND EXISTS (SELECT 1 FROM storage_tasks WHERE target_id = storage_files.id AND kind = ? AND state = ?)", "deleted", now, "delete", "done").
		Order("next_sweep_at, id").
		Limit(100).
		Find(&files).
		Error; err != nil {
		return err
	}
	for _, file := range files {
		needsDelete := file.BackendID != w.adapter.BackendID()
		probeResult := "error"
		if !needsDelete {
			_, statErr := w.adapter.Stat(ctx, file.Bucket, file.ObjectKey)
			needsDelete = statErr == nil || !errors.Is(statErr, client.ErrObjectNotFound)
			if statErr == nil {
				probeResult = "object"
			}
			if errors.Is(statErr, client.ErrObjectNotFound) {
				hasParts, probeErr := w.adapter.HasIncompleteUploads(ctx, file.Bucket, file.ObjectKey)
				needsDelete = hasParts || probeErr != nil
				if probeErr == nil {
					probeResult = "clean"
					if hasParts {
						probeResult = "multipart"
					}
				}
			}
		}
		telemetry.TombstoneProbe(probeResult)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// An inconclusive probe is handed to the bounded deletion executor too,
		// so a broken location cannot starve later tombstones or retry forever.
		err = workerTransaction(ctx, w.database, func(tx *db.DB) error {
			var current table.StorageFile
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&current, "id = ?", file.ID).
				Error; e != nil {
				return e
			}
			now, e := table.DatabaseNow(tx)
			if e != nil {
				return e
			}
			if current.State != "deleted" || current.NextSweepAt == nil ||
				now.Before(*current.NextSweepAt) {
				return nil
			}
			var task table.StorageTask
			if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("kind = ? AND target_id = ?", "delete", file.ID).
				First(&task).
				Error; e != nil {
				return e
			}
			if task.State != "done" {
				return nil
			}
			next := now.Add(w.config.SweepInterval)
			current.NextSweepAt = &next
			if e = tx.Save(&current).Error; e != nil {
				return e
			}
			if needsDelete {
				task.State = "pending"
				task.NextRunAt = now
				task.Attempts = 0
				task.RunGeneration++
				task.Revision++
				return tx.Save(&task).Error
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Execute checks ownership before I/O and fences terminal metadata in a short transaction.
func (w *Worker) Execute(ctx context.Context, task *table.StorageTask) error {
	if task.Kind != "delete" {
		return errors.New("unsupported storage task kind")
	}
	var file table.StorageFile
	waiting := false
	err := workerTransaction(ctx, w.database, func(tx *db.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&file, "id = ?", task.TargetID).
			Error; e != nil {
			return e
		}
		row, now, e := lease(tx, task.ID, task.LeaseToken)
		if e != nil {
			return e
		}
		if file.State != "deleting" && file.State != "deleted" {
			return errors.New("file is not eligible for deletion")
		}
		if file.LastGrantExpiresAt != nil && now.Before(*file.LastGrantExpiresAt) {
			row.State = "pending"
			row.NextRunAt = *file.LastGrantExpiresAt
			row.LeaseToken = ""
			row.LeaseUntil = nil
			row.Revision++
			waiting = true
			return tx.Save(row).Error
		}
		return nil
	})
	if err != nil || waiting {
		return err
	}
	if file.BackendID != w.adapter.BackendID() {
		return errors.New("storage file backend differs from configured backend")
	}
	if err = w.adapter.Delete(ctx, file.Bucket, file.ObjectKey); err != nil {
		return err
	}
	completed := false
	err = workerTransaction(ctx, w.database, func(tx *db.DB) error {
		var current table.StorageFile
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&current, "id = ?", file.ID).
			Error; e != nil {
			return e
		}
		row, now, e := lease(tx, task.ID, task.LeaseToken)
		if e != nil {
			return e
		}
		if current.State != "deleting" && current.State != "deleted" {
			return errors.New("file is not eligible for deletion")
		}
		if current.LastGrantExpiresAt != nil && now.Before(*current.LastGrantExpiresAt) {
			return errors.New("write grant deadline changed during deletion")
		}
		current.State = "deleted"
		if current.DeletedAt == nil {
			current.DeletedAt = &now
		}
		next := now.Add(w.config.SweepInterval)
		current.NextSweepAt = &next
		if e = tx.Save(&current).Error; e != nil {
			return e
		}
		row.State = "done"
		row.LeaseToken = ""
		row.LeaseUntil = nil
		row.Revision++
		if e = tx.Save(row).Error; e != nil {
			return e
		}
		completed = true
		return nil
	})
	if err != nil && completed {
		return ErrOutcomeUnknown
	}
	return err
}

// Run owns all executors and waits for their cancellation and renewal goroutines.
func (w *Worker) Run(ctx context.Context) error {
	unregister, err := telemetry.RegisterTasks(ctx, "storage", []string{"delete"}, w.taskSnapshot)
	if err != nil {
		logger.Errorf(ctx, "storage task metrics registration failed: %v", err)
	}
	defer unregister()
	var wg sync.WaitGroup
	for range w.config.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(w.config.ScanInterval)
			defer ticker.Stop()
			for ctx.Err() == nil {
				if err := w.Scan(ctx); err != nil && ctx.Err() == nil {
					logger.Errorf(ctx, "storage task scan failed: %v", err)
				}
				for ctx.Err() == nil {
					task, err := w.Claim(ctx)
					if err != nil {
						if ctx.Err() == nil {
							logger.Errorf(ctx, "storage task claim failed: %v", err)
						}
						break
					}
					if task == nil {
						break
					}
					w.runTask(ctx, task)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

func (w *Worker) runTask(ctx context.Context, task *table.StorageTask) {
	taskCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(w.config.RenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-taskCtx.Done():
				return
			case <-ticker.C:
				if err := w.Renew(taskCtx, task.ID, task.LeaseToken); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := w.Execute(taskCtx, task)
	cancel()
	<-done
	if errors.Is(err, ErrOutcomeUnknown) {
		logger.Errorf(
			ctx,
			"storage task %s kind=%s target=%s outcome unknown; lease recovery will reconcile",
			task.ID,
			task.Kind,
			task.TargetID,
		)
		return
	}
	if err != nil && ctx.Err() == nil && !errors.Is(err, ErrLeaseLost) && !errors.Is(err, context.Canceled) {
		if e := w.Fail(ctx, task.ID, task.LeaseToken, err); e != nil {
			logger.Errorf(ctx, "storage task %s failure recording failed: %v", task.ID, e)
		} else {
			logger.Errorf(
				ctx,
				"storage task %s kind=%s target=%s execution failed: %v",
				task.ID,
				task.Kind,
				task.TargetID,
				err,
			)
		}
	}
}
