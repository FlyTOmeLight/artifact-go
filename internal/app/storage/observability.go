package storage

import (
	"context"
	"database/sql"

	table "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
)

func (w *Worker) taskSnapshot(ctx context.Context) (telemetry.TaskSnapshot, error) {
	var out telemetry.TaskSnapshot
	database := w.database.WithContext(ctx)
	now, err := table.DatabaseNow(database)
	if err != nil {
		return out, err
	}
	out.Now = now
	var groups []struct {
		Kind, State string
		Count       int64
		OldestDue   sql.NullTime
	}
	err = database.Model(&table.StorageTask{}).Select(`kind, state, COUNT(*) AS count,
 MIN(CASE WHEN state = 'pending' AND next_run_at <= ? AND not_before <= ?
 THEN CASE WHEN next_run_at > not_before THEN next_run_at ELSE not_before END
 WHEN state = 'running' AND lease_until <= ? THEN lease_until ELSE NULL END) AS oldest_due`, now, now, now).
		Where("state IN ?", []string{"pending", "running", "failed"}).Group("kind, state").Scan(&groups).Error
	if err != nil {
		return out, err
	}
	for _, g := range groups {
		out.Groups = append(
			out.Groups,
			telemetry.TaskGroup{Kind: g.Kind, State: g.State, Count: g.Count, OldestDue: g.OldestDue.Time},
		)
	}

	return out, nil
}
