package doctor

import (
	"time"

	"github.com/FlyTOmeLight/server-go/db"
)

type deletedFile struct {
	ID          string
	NextSweepAt *time.Time
	TaskID      string
	TaskState   string
}

func storageChecks(tx *db.DB, limit int) ([]Check, error) {
	type boundFile struct {
		ID        string
		HasActive bool
	}
	bound, boundCheck, err := candidates[boundFile](
		tx.Table("storage_files").
			Select("id, EXISTS (SELECT 1 FROM storage_references WHERE file_id = storage_files.id AND state = ?) AS has_active", "active").
			Where("state = ?", "bound").Order("id"),
		"bound_file_active_reference", limit,
	)
	if err != nil {
		return nil, err
	}
	for _, file := range bound {
		if !file.HasActive {
			boundCheck.Findings = append(boundCheck.Findings, Finding{
				ID: file.ID, Severity: "error", Message: "bound file has no active storage reference",
			})
		}
	}
	deleted, deletedCheck, err := candidates[deletedFile](
		tx.Table("storage_files AS files").
			Select("files.id, files.next_sweep_at, tasks.id AS task_id, tasks.state AS task_state").
			Joins("LEFT JOIN storage_tasks AS tasks ON tasks.target_id = files.id AND tasks.kind = ?", "delete").
			Where("files.state = ?", "deleted").Order("files.id"),
		"deleted_file_cleanup", limit,
	)
	if err != nil {
		return nil, err
	}
	for _, file := range deleted {
		if finding := file.finding(); finding != nil {
			deletedCheck.Findings = append(deletedCheck.Findings, *finding)
		}
	}
	return []Check{boundCheck, deletedCheck}, nil
}

func (file deletedFile) finding() *Finding {
	finding := &Finding{ID: file.ID, TaskID: file.TaskID, Severity: "error"}
	if file.TaskID == "" {
		finding.Message = "deleted file has no deletion task; a sweep timestamp alone cannot schedule cleanup"
		return finding
	}
	switch file.TaskState {
	case "pending", "running":
		return nil
	case "done":
		if file.NextSweepAt != nil && !file.NextSweepAt.IsZero() {
			return nil
		}
		finding.Message = "completed deletion has no next sweep scheduled"
	case "failed":
		finding.Severity = "warning"
		finding.Message = "deletion re-sweep failed; automatic sweeping is blocked until task recovery"
	default:
		finding.Message = "deleted file has an unrecognized deletion task state"
	}
	return finding
}
