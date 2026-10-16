package backgroundtask

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
)

func (w *Worker) WithReferences(client storage.ReferenceService) *Worker {
	w.references = client
	return w
}

// scanReferences only aborts expired work; it never publishes on a client's behalf.
func (w *Worker) scanReferences(ctx context.Context) error {
	var due []table.ReferenceChange
	now, err := table.DatabaseNow(w.database.WithContext(ctx))
	if err != nil {
		return err
	}
	if err = w.database.WithContext(ctx).
		Where("operation_kind IN ? AND state = ? AND deadline <= ?", []string{"create-version", "create_draft", "update_draft"}, "preparing", now).
		Order("artifact_id").
		Limit(100).
		Find(&due).
		Error; err != nil {
		return err
	}
	for _, candidate := range due {
		if err = transaction(ctx, w.database, func(tx *db.DB) error {
			var a table.Artifact
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&a, "id = ?", candidate.ArtifactID).
				Error; e != nil {
				return e
			}
			var current table.ReferenceChange
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("artifact_id = ? AND actor_id = ? AND operation_kind = ? AND request_key = ?", candidate.ArtifactID, candidate.ActorID, candidate.OperationKind, candidate.RequestKey).
				First(&current).
				Error; e != nil {
				return e
			}
			at, e := table.DatabaseNow(tx)
			if e != nil {
				return e
			}
			if current.AttemptID != candidate.AttemptID || current.State != "preparing" ||
				at.Before(current.Deadline) {
				return nil
			}
			return version.AbortReferenceChange(tx, &current, at)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) executeRelease(ctx context.Context, task *table.BackgroundTask) error {
	if w.references == nil {
		return errors.New("storage references are not configured")
	}
	var plan version.ReferencePlan
	if err := json.Unmarshal(task.Payload, &plan); err != nil {
		return errors.New("invalid reference release task")
	}
	if len(plan.References) == 0 {
		return errors.New("empty reference release task")
	}
	if err := transaction(
		ctx,
		w.database,
		func(tx *db.DB) error { _, _, e := lease(tx, task.ID, task.LeaseToken); return e },
	); err != nil {
		return err
	}
	complete := true
	pairs := plan.ReferencePairs()
	for start := 0; start < len(pairs); start += 1000 {
		batch := pairs[start:min(start+1000, len(pairs))]
		if _, err := w.references.Release(ctx, plan.Scope, batch); err != nil {
			return err
		}
		statuses, err := w.references.Statuses(ctx, plan.Scope, batch)
		if err != nil {
			return err
		}
		if len(statuses) != len(batch) {
			return errors.New("invalid reference release receipt")
		}
		for _, status := range statuses {
			if status.State != "released" || !status.ReleaseComplete {
				complete = false
			}
		}
	}
	committed := false
	err := transaction(ctx, w.database, func(tx *db.DB) error {
		// Match publication/deletion lock order before the final lease fence.
		var target table.Version
		if plan.VersionID != "" {
			if e := tx.First(&target, "id = ?", plan.VersionID).Error; e != nil {
				return e
			}
			var a table.Artifact
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&a, "id = ?", target.ArtifactID).
				Error; e != nil {
				return e
			}
		}
		row, now, e := lease(tx, task.ID, task.LeaseToken)
		if e != nil {
			return e
		}
		if complete && plan.VersionID != "" {
			if e = tx.Model(&table.Version{}).
				Where("id = ? AND state = ?", plan.VersionID, "deleting").
				Updates(map[string]any{"state": "deleted", "deleted_at": now}).
				Error; e != nil {
				return e
			}
		}
		row.LeaseToken = ""
		row.LeaseUntil = nil
		row.Revision++
		if complete {
			row.State = "done"
			row.LastError = ""
		} else {
			row.State = "pending"
			row.NextRunAt = now.Add(max(w.config.ScanInterval, time.Second))
		}
		if e = tx.Save(row).Error; e != nil {
			return e
		}
		committed = true
		return nil
	})
	if err != nil && committed {
		return ErrOutcomeUnknown
	}
	return err
}
