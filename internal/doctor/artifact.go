package doctor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
)

type releaseTask struct {
	ID    string
	State string
}

func artifactChecks(tx *db.DB, now time.Time, limit int) ([]Check, error) {
	versions, err := deletingVersions(tx, limit)
	if err != nil {
		return nil, err
	}
	type attempt struct{ ID string }
	expired, preparingCheck, err := candidates[attempt](
		tx.Table("reference_changes").Select("attempt_id AS id").
			Where("state = ? AND deadline <= ?", "preparing", now).
			Order("deadline, artifact_id, actor_id, operation_kind, request_key"),
		"expired_preparing_reference_change", limit,
	)
	if err != nil {
		return nil, err
	}
	for _, change := range expired {
		preparingCheck.Findings = append(preparingCheck.Findings, Finding{
			ID:       change.ID,
			Severity: "warning",
			Message:  "preparing reference change is due for worker abort; expiry alone does not imply corruption",
		})
	}
	type retentionTask struct{ ID, TargetID string }
	failed, retentionCheck, err := candidates[retentionTask](
		tx.Table("background_tasks").Select("id, target_id").
			Where("kind = ? AND state = ?", "retention", "failed").Order("id"),
		"failed_retention_evaluation", limit,
	)
	if err != nil {
		return nil, err
	}
	for _, task := range failed {
		retentionCheck.Findings = append(retentionCheck.Findings, Finding{
			ID: task.TargetID, TaskID: task.ID, Severity: "warning",
			Message: "failed retention task blocks subsequent scheduled evaluation until task recovery",
		})
	}
	return []Check{versions, preparingCheck, retentionCheck}, nil
}

func deletingVersions(tx *db.DB, limit int) (Check, error) {
	type version struct{ ID string }
	versions, check, err := candidates[version](
		tx.Table("versions").Select("id").Where("state = ?", "deleting").Order("id"),
		"deleting_version_reference_release", limit,
	)
	if err != nil || len(versions) == 0 {
		return check, err
	}
	releases := make(map[string]releaseTask, len(versions))
	for _, version := range versions {
		releases[version.ID] = releaseTask{}
	}
	if err = readReleases(tx, releases); err != nil {
		return check, err
	}
	for _, version := range versions {
		task := releases[version.ID]
		if task.ID == "" {
			check.Findings = append(check.Findings, Finding{
				ID:       version.ID,
				Severity: "error",
				Message:  "deleting version has no unfinished reference-release task with matching payload versionID",
			})
		} else if task.State == "failed" {
			check.Findings = append(check.Findings, Finding{
				ID:       version.ID,
				TaskID:   task.ID,
				Severity: "warning",
				Message:  "reference-release task failed; version deletion is blocked until task recovery",
			})
		}
	}
	return check, nil
}

func readReleases(tx *db.DB, releases map[string]releaseTask) error {
	rows, err := tx.Table("background_tasks").Select("id, state, payload").
		Where("kind = ? AND state IN ?", "reference-release", []string{"pending", "running", "failed"}).
		Order("id").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var task releaseTask
		var payload []byte
		if err = rows.Scan(&task.ID, &task.State, &payload); err != nil {
			return err
		}
		var plan version.ReferencePlan
		if err = json.Unmarshal(payload, &plan); err != nil {
			return fmt.Errorf("reference-release task %s has an invalid payload: %w", task.ID, err)
		}
		if len(plan.References) == 0 {
			return fmt.Errorf("reference-release task %s has an empty reference release plan", task.ID)
		}
		previous, relevant := releases[plan.VersionID]
		if relevant && (previous.ID == "" || previous.State == "failed" && task.State != "failed") {
			releases[plan.VersionID] = task
		}
	}
	return rows.Err()
}
