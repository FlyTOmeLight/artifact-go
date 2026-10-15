package backgroundtask

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
)

// Scan schedules due retention work without reviving exhausted tasks.
func (w *Worker) Scan(ctx context.Context) error {
	if err := w.scanReferences(ctx); err != nil {
		return err
	}
	return transaction(ctx, w.database, func(tx *db.DB) error {
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		var artifacts []table.Artifact
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("next_retention_at <= ? AND NOT EXISTS (SELECT 1 FROM background_tasks WHERE kind = ? AND target_id = artifacts.id AND state <> ?)", now, "retention", "done").
			Order("id").
			Limit(100).
			Find(&artifacts).
			Error; err != nil {
			return err
		}
		for _, a := range artifacts {
			var task table.BackgroundTask
			err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("kind = ? AND target_id = ?", "retention", a.ID).
				First(&task).
				Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				task = table.BackgroundTask{
					ID:             uuid.NewString(),
					Kind:           "retention",
					TargetID:       a.ID,
					Payload:        []byte("{}"),
					RecoveryEvents: []byte("[]"),
					State:          "pending",
					NextRunAt:      now,
					NotBefore:      now,
					Revision:       1,
					RunGeneration:  1,
				}
				if err = tx.Create(&task).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if task.State == "done" {
				task.State = "pending"
				task.NextRunAt = now
				task.Attempts = 0
				task.RunGeneration++
				task.Revision++
				if err = tx.Save(&task).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Execute atomically fences the worker, applies retention and records completion.
func (w *Worker) Execute(ctx context.Context, task *table.BackgroundTask) error {
	if task.Kind == "reference-release" {
		return w.executeRelease(ctx, task)
	}
	if task.Kind != "retention" {
		return errors.New("unsupported artifact task kind")
	}
	completed := false
	err := transaction(ctx, w.database, func(tx *db.DB) error {
		var artifact table.Artifact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&artifact, "id = ?", task.TargetID).
			Error; err != nil {
			return err
		}
		var initial table.BackgroundTask
		if err := tx.First(&initial, "id = ?", task.ID).Error; err != nil {
			return err
		}
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		if initial.State != "running" || initial.LeaseToken != task.LeaseToken || initial.LeaseUntil == nil ||
			!now.Before(*initial.LeaseUntil) {
			return ErrLeaseLost
		}
		if err != nil {
			return err
		}
		reasons, err := version.ApplyRetention(tx, artifact.ID, now)
		if err != nil {
			return err
		}
		// The task lock is acquired only at the final fence, so renewals can
		// proceed while retention holds its Artifact lock.
		row, now, err := lease(tx, task.ID, task.LeaseToken)
		if err != nil {
			return err
		}
		var policy table.RetentionPolicy
		if err = tx.First(
			&policy,
			"artifact_id = ?",
			artifact.ID,
		).Error; err != nil &&
			!errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var next *time.Time
		if err == nil && policy.Enabled {
			value := now.Add(w.config.ScanInterval)
			next = &value
		}
		if err = tx.Model(&artifact).Update("next_retention_at", next).Error; err != nil {
			return err
		}
		row.Result, err = json.Marshal(reasons)
		if err != nil {
			return err
		}
		row.State = "done"
		row.LeaseToken = ""
		row.LeaseUntil = nil
		row.Revision++
		row.LastError = ""
		if err = tx.Save(row).Error; err != nil {
			return err
		}
		completed = true
		return nil
	})
	if err != nil && completed {
		return ErrOutcomeUnknown
	}
	return err
}

// Run owns the worker goroutines and waits for all of them after cancellation.
func (w *Worker) Run(ctx context.Context) error {
	unregister, err := telemetry.RegisterTasks(
		ctx,
		"artifact",
		[]string{"retention", "reference-release"},
		w.taskSnapshot,
	)
	if err != nil {
		logger.Errorf(ctx, "artifact task metrics registration failed: %v", err)
	}
	defer unregister()
	var wg sync.WaitGroup
	for range w.config.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(w.config.ScanInterval)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				if err := w.Scan(ctx); err != nil && ctx.Err() == nil {
					logger.Errorf(ctx, "artifact task scan failed: %v", err)
				}
				for ctx.Err() == nil {
					task, err := w.Claim(ctx)
					if err != nil {
						if ctx.Err() == nil {
							logger.Errorf(ctx, "artifact task claim failed: %v", err)
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

func (w *Worker) runTask(ctx context.Context, task *table.BackgroundTask) {
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
			"artifact task %s kind=%s target=%s outcome unknown; lease recovery will reconcile",
			task.ID,
			task.Kind,
			task.TargetID,
		)
		return
	}
	if err != nil && ctx.Err() == nil && !errors.Is(err, ErrLeaseLost) && !errors.Is(err, context.Canceled) {
		if failErr := w.Fail(ctx, task.ID, task.LeaseToken, err); failErr != nil {
			logger.Errorf(ctx, "artifact task %s failure recording failed: %v", task.ID, failErr)
		} else {
			logger.Errorf(
				ctx,
				"artifact task %s kind=%s target=%s execution failed: %v",
				task.ID,
				task.Kind,
				task.TargetID,
				err,
			)
		}
	}
}
