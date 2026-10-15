package draft

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/cursor"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type Limits struct {
	MaxPayloadBytes, MaxAnnotationsBytes, MaxRequestBodyBytes int64
	MaxFilesPerVersion                                        int
}

type Service struct {
	database *db.DB
	limits   Limits
	handoff  *storagehandoff.Client
	recheck  func(*http.Request) (scope.Identity, error)
}

func NewService(database *db.DB, limits Limits) *Service {
	return &Service{database: database, limits: limits}
}

func invalid(message string) error { return serrors.NewError(400, "InvalidDraftRequest", message) }

func databaseError(err error) error {
	var deleted *deletedError
	var app *serrors.Error
	if errors.As(err, &deleted) || errors.As(err, &app) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return serrors.NewError(404, "ResourceNotFound", "resource not found")
	}
	return serrors.ErrorInternalError
}

type deletedError struct {
	identity api.DraftIdentity
	applied  *int64
}

func (e *deletedError) Error() string { return "draft is deleted" }

func artifact(tx *db.DB, i scope.Identity, id, strength string) (table.Artifact, error) {
	var a table.Artifact
	q := tx.Where("tenant_id = ? AND scope_key = ?", i.TenantID, i.Key())
	if strength != "" {
		q = q.Clauses(clause.Locking{Strength: strength})
	}
	started := time.Now()
	err := q.First(&a, "id = ?", id).Error
	if strength != "" {
		telemetry.ObserveArtifactLock("draft", time.Since(started))
	}
	return a, err
}

func readDraft(tx *db.DB, artifactID, id, strength string) (table.Draft, error) {
	var d table.Draft
	q := tx.Where("artifact_id = ? AND id = ?", artifactID, id)
	if strength != "" {
		q = q.Clauses(clause.Locking{Strength: strength})
	}
	err := q.First(&d).Error
	return d, err
}

func summary(d table.Draft) api.DraftSummary {
	return api.DraftSummary{
		DraftIdentity: api.DraftIdentity{ID: d.ID, ArtifactID: d.ArtifactID},
		Revision:      d.Revision,
		CreatedAt:     d.CreatedAt.UTC(),
		UpdatedAt:     d.UpdatedAt.UTC(),
	}
}

func response(tx *db.DB, d table.Draft) (api.DraftResponse, error) {
	out := api.DraftResponse{
		DraftSummary: summary(d),
		Payload:      d.Payload,
		Annotations:  d.Annotations,
		Files:        []api.FileResponse{},
	}
	var files []table.DraftFile
	if err := tx.Where("draft_id = ?", d.ID).Order("logical_path").Find(&files).Error; err != nil {
		return out, err
	}
	for _, f := range files {
		var checksum *api.FileChecksum
		if len(f.DeclaredChecksum) > 0 {
			if err := json.Unmarshal(f.DeclaredChecksum, &checksum); err != nil {
				return out, err
			}
		}
		out.Files = append(
			out.Files,
			api.FileResponse{
				FileID:           f.FileID,
				Path:             f.LogicalPath,
				Size:             f.Size,
				ContentType:      f.ContentType,
				DeclaredChecksum: checksum,
			},
		)
	}
	return out, nil
}

func active(i scope.Identity, a table.Artifact) error {
	if err := i.RequireActive(); err != nil {
		return err
	}
	if a.State != "active" {
		return serrors.NewError(409, "ArtifactArchived", "artifact is archived")
	}
	return nil
}

func (s *Service) size(n normalized) error {
	if int64(len(n.payload)) > s.limits.MaxPayloadBytes {
		return serrors.NewError(413, "PayloadTooLarge", "payload exceeds configured limit")
	}
	if int64(len(n.annotations)) > s.limits.MaxAnnotationsBytes {
		return serrors.NewError(413, "AnnotationsTooLarge", "annotations exceeds configured limit")
	}
	return nil
}

func mismatch() error {
	return serrors.NewError(409, "IdempotentParameterMismatch", "requestKey was used with different parameters")
}

func unknown() error {
	return serrors.NewError(
		503,
		"OperationOutcomeUnknown",
		"outcome cannot be confirmed; retry with the same requestKey and parameters",
	)
}

func options() db.TransactionOptions {
	return db.TransactionOptions{
		Isolation: sql.LevelReadCommitted,
		Retry:     &db.TransactionRetry{MaxAttempts: 3, Timeout: 5 * time.Second, Delay: 10 * time.Millisecond},
	}
}

func createReceipt(tx *db.DB, a, actor, key, hash string) (*api.DraftResponse, bool, error) {
	var r table.DraftCreateRequest
	err := tx.Where("artifact_id = ? AND actor_id = ? AND request_key = ?", a, actor, key).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if r.RequestHash != hash {
		return nil, true, mismatch()
	}
	d, err := readDraft(tx, a, r.DraftID, "SHARE")
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, true, &deletedError{identity: api.DraftIdentity{ArtifactID: a, ID: r.DraftID}}
	}
	if err != nil {
		return nil, true, err
	}
	out, err := response(tx, d)
	return &out, true, err
}

func updateReceipt(tx *db.DB, a, actor, key, hash string) (*api.UpdateDraftResponse, bool, error) {
	var r table.ReferenceChange
	err := tx.Where("artifact_id = ? AND actor_id = ? AND operation_kind = ? AND request_key = ?", a, actor, "update_draft", key).
		First(&r).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if r.RequestHash != hash {
		return nil, true, mismatch()
	}
	if r.State == "aborted" || r.State == "preparing" {
		return nil, false, nil
	}
	if r.State != "committed" {
		return nil, true, serrors.NewError(409, "RequestInProgress", "request is in progress")
	}
	if r.AppliedRevision == nil {
		return nil, true, serrors.ErrorInternalError
	}
	d, err := readDraft(tx, a, r.DraftID, "SHARE")
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, true, &deletedError{
			identity: api.DraftIdentity{ArtifactID: a, ID: r.DraftID},
			applied:  r.AppliedRevision,
		}
	}
	if err != nil {
		return nil, true, err
	}
	snapshot, err := response(tx, d)
	out := api.UpdateDraftResponse{AppliedRevision: *r.AppliedRevision, Draft: snapshot}
	return &out, true, err
}

// confirm uses a bounded read-only transaction. A failed read-only COMMIT cannot
// invalidate a complete lock-protected receipt and snapshot already observed.
func (s *Service) confirm(ctx context.Context, i scope.Identity, a string, read func(*db.DB) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var observed error
	complete := false
	_ = db.Transaction(
		ctx,
		s.database,
		db.TransactionOptions{Isolation: sql.LevelReadCommitted},
		func(tx *db.DB) error {
			if _, err := artifact(tx, i, a, "SHARE"); err != nil {
				return err
			}
			observed = read(tx)
			complete = true
			return observed
		},
	)
	if complete {
		var gone *deletedError
		var problem *serrors.Error
		if observed == nil || errors.As(observed, &gone) ||
			(errors.As(observed, &problem) && (problem.Code == "IdempotentParameterMismatch" || problem.Code == "RevisionConflict")) {
			return observed
		}
	}
	return unknown()
}

func (s *Service) Get(c *core.Context, req *api.GetDraftRequest) (*api.DraftResponse, error) {
	i, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	var out api.DraftResponse
	err = db.Transaction(c.Request.Context(), s.database, options(), func(tx *db.DB) error {
		if _, err := artifact(tx, i, req.ArtifactID, "SHARE"); err != nil {
			return err
		}
		d, err := readDraft(tx, req.ArtifactID, req.DraftID, "SHARE")
		if err != nil {
			return err
		}
		out, err = response(tx, d)
		return err
	})
	if err != nil {
		return nil, databaseError(err)
	}
	return &out, nil
}

func (s *Service) List(c *core.Context, req *api.ListDraftsRequest) (*api.ListDraftsResponse, error) {
	i, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	database := s.database.WithContext(c.Request.Context())
	if _, err = artifact(database, i, req.ArtifactID, ""); err != nil {
		return nil, databaseError(err)
	}
	q := cursor.Query{TenantID: i.TenantID, ScopeKey: i.Key(), ArtifactID: req.ArtifactID, Order: "draft-id-asc"}
	after, err := cursor.Decode(req.Cursor, q)
	if err != nil {
		return nil, serrors.NewError(400, "InvalidCursor", err.Error())
	}
	query := database.Select("id", "artifact_id", "revision", "created_at", "updated_at").
		Where("artifact_id = ?", req.ArtifactID)
	if after != "" {
		query = query.Where("id > ?", after)
	}
	var rows []table.Draft
	if err = query.Order("id ASC").Limit(req.Limit + 1).Find(&rows).Error; err != nil {
		return nil, databaseError(err)
	}
	out := api.ListDraftsResponse{Items: []api.DraftSummary{}}
	if len(rows) > req.Limit {
		rows = rows[:req.Limit]
		out.NextCursor = cursor.Encode(q, rows[len(rows)-1].ID)
	}
	for _, d := range rows {
		out.Items = append(out.Items, summary(d))
	}
	return &out, nil
}

func (s *Service) Delete(c *core.Context, req *api.DeleteDraftRequest) error {
	i, err := scope.From(c)
	if err != nil {
		return err
	}
	if req.ExpectedRevision == nil || *req.ExpectedRevision <= 0 {
		return invalid("expectedRevision must be positive")
	}
	completed := false
	err = db.Transaction(c.Request.Context(), s.database, options(), func(tx *db.DB) error {
		completed = false
		if _, err := artifact(tx, i, req.ArtifactID, "UPDATE"); err != nil {
			return err
		}
		d, err := readDraft(tx, req.ArtifactID, req.DraftID, "UPDATE")
		if err != nil {
			return err
		}
		if d.Revision != *req.ExpectedRevision {
			return serrors.NewError(409, "RevisionConflict", "draft revision does not match")
		}
		var files []table.DraftFile
		if err = tx.Where("draft_id = ?", d.ID).Find(&files).Error; err != nil {
			return err
		}
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		plan := version.ReferencePlan{Scope: storagehandoff.Scope{TenantID: i.TenantID, ProjectID: i.ProjectID}}
		for _, f := range files {
			plan.References = append(
				plan.References,
				version.PlannedReference{
					Reference: storagehandoff.Reference{
						ReferenceID: f.ReferenceID,
						FileID:      f.FileID,
					},
				},
			)
		}
		if len(plan.References) > 0 {
			if err = version.QueueReferenceRelease(tx, uuid.NewString(), plan, now); err != nil {
				return err
			}
		}
		if err = tx.Where("draft_id = ?", d.ID).Delete(&table.DraftFile{}).Error; err != nil {
			return err
		}
		if err = tx.Delete(&d).Error; err != nil {
			return err
		}
		completed = true
		return nil
	})
	if err != nil {
		if completed {
			return serrors.NewError(
				503,
				"OperationOutcomeUnknown",
				"deletion outcome cannot be confirmed; read the draft and reconcile its current state with the original expectedRevision",
			)
		}
		return databaseError(err)
	}
	return nil
}
