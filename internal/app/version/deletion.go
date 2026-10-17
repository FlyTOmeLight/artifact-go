package version

import (
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/google/uuid"

	"github.com/FlyTOmeLight/artifact-go/internal/app/alias"
	"github.com/FlyTOmeLight/artifact-go/internal/app/retention"
	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

// Delete removes content while retaining the immutable version identity and receipt.
func (s *Service) Delete(c *core.Context, req *api.DeleteVersionRequest) (*api.DeleteVersionResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	var result api.DeleteVersionResponse
	completed := false
	err = db.Transaction(c.Request.Context(), s.database, db.TransactionOptions{
		Isolation: sql.LevelReadCommitted,
		Retry:     &db.TransactionRetry{MaxAttempts: 3, Timeout: 5 * time.Second, Delay: 10 * time.Millisecond},
	}, func(tx *db.DB) error {
		completed = false
		a, err := findArtifact(tx, identity, req.ArtifactID, true)
		if err != nil {
			return err
		}
		var row table.Version
		if err = tx.Where("artifact_id = ? AND id = ?", a.ID, req.VersionID).First(&row).Error; err != nil {
			return err
		}
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		if err = markDeleting(tx, &row, false, now); err != nil {
			return err
		}
		result = api.DeleteVersionResponse{
			VersionIdentity: versionIdentity(a, row),
			State:           api.VersionState(row.State),
		}
		completed = true
		return nil
	})
	if err != nil {
		if completed {
			return nil, serrors.NewError(
				503,
				"OperationOutcomeUnknown",
				"deletion outcome cannot be confirmed; repeat deletion to confirm its state",
			)
		}
		return nil, databaseError(err)
	}
	return &result, nil
}

// markDeleting requires the owning Artifact lock. Automatic callers must have
// selected this version with Plan using the same locked transaction.
func markDeleting(tx *db.DB, row *table.Version, automatic bool, now time.Time) error {
	if row.State == "deleted" || row.State == "deleting" {
		return nil
	}
	if row.Protected {
		return serrors.NewError(409, "VersionProtected", "protected version cannot be deleted")
	}
	var refs []table.Alias
	if err := tx.Where("artifact_id = ? AND version_id = ?", row.ArtifactID, row.ID).
		Order("name ASC").
		Find(&refs).
		Error; err != nil {
		return err
	}
	if !automatic && len(refs) > 0 {
		return serrors.NewError(409, "VersionReferenced", "version is referenced by an alias")
	}
	for _, ref := range refs {
		if _, err := alias.Delete(tx, row.ArtifactID, ref.Name, ref.Revision); err != nil {
			return err
		}
	}
	if row.Revision == math.MaxInt64 {
		return serrors.NewError(409, "RevisionExhausted", "version revision exhausted")
	}
	var files []table.VersionFile
	if err := tx.Where("version_id = ?", row.ID).Find(&files).Error; err != nil {
		return err
	}
	row.State = "deleted"
	if len(files) > 0 {
		var artifact table.Artifact
		if err := tx.First(&artifact, "id = ?", row.ArtifactID).Error; err != nil {
			return err
		}
		plan := ReferencePlan{
			Scope: storage.Scope{
				TenantID:    artifact.TenantID,
				ProjectID:   artifact.ProjectID,
				OwnerUserID: artifact.OwnerUserID,
			},
			VersionID: row.ID,
		}
		for _, file := range files {
			plan.References = append(
				plan.References,
				PlannedReference{
					Reference: storage.Reference{
						ReferenceID: file.ReferenceID,
						FileID:      file.FileID,
					},
				},
			)
		}
		if err := QueueReferenceRelease(tx, uuid.NewString(), plan, now); err != nil {
			return err
		}
		if err := tx.Where("version_id = ?", row.ID).Delete(&table.VersionFile{}).Error; err != nil {
			return err
		}
		row.State = "deleting"
	}
	row.Payload = []byte(`{}`)
	row.Annotations = []byte(`{}`)
	row.DeletedAt = &now
	row.Revision++
	return tx.Model(row).
		Updates(map[string]any{"state": row.State, "payload": row.Payload, "annotations": row.Annotations, "deleted_at": now, "revision": row.Revision}).
		Error
}

// ApplyRetention recomputes a policy under the caller's Artifact lock. Ordinary
// protection blocking is returned as reasons, not as a failed worker execution.
func ApplyRetention(tx *db.DB, artifactID string, now time.Time) ([]string, error) {
	policy, err := retention.Load(tx, artifactID)
	if err != nil {
		return nil, err
	}
	facts, err := retention.LoadFacts(tx, artifactID)
	if err != nil {
		return nil, err
	}
	plan := retention.Plan(retention.PlanInput{Policy: policy, Versions: facts, Now: now})
	if err = applyCandidates(tx, artifactID, plan.Candidates, now); err != nil {
		return nil, err
	}
	if policy.Revision > 0 {
		result, err := json.Marshal(plan.BlockingReasons)
		if err != nil {
			return nil, err
		}
		if err = tx.Model(&table.RetentionPolicy{}).
			Where("artifact_id = ?", artifactID).
			Updates(map[string]any{"last_evaluated_at": now, "last_result": result}).
			Error; err != nil {
			return nil, err
		}
	}
	return plan.BlockingReasons, nil
}

func applyCandidates(tx *db.DB, artifactID string, candidates []retention.Decision, now time.Time) error {
	for _, candidate := range candidates {
		var row table.Version
		if err := tx.Where("artifact_id = ? AND id = ? AND state = ?", artifactID, candidate.VersionID, "ready").
			First(&row).
			Error; err != nil {
			return err
		}
		if err := markDeleting(tx, &row, true, now); err != nil {
			return err
		}
	}
	return nil
}
