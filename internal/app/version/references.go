package version

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

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/reference"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

// ReferencePlan is persisted before any Bind; failed attempts retain their original deadline.
type PlannedReference struct {
	storage.Reference
	OriginalExpiresAt time.Time `json:"originalExpiresAt"`
}

type ReferencePlan struct {
	Scope      storage.Scope      `json:"scope"`
	References []PlannedReference `json:"references"`
	VersionID  string             `json:"versionID,omitempty"`
}

func (p ReferencePlan) ReferencePairs() []storage.Reference {
	refs := make([]storage.Reference, len(p.References))
	for i, item := range p.References {
		refs[i] = item.Reference
	}
	return refs
}

// Bind preserves every planned identity across partial batch success and retries.
func (p ReferencePlan) Bind(ctx context.Context, client storage.ReferenceService) ([]storage.BindResult, error) {
	pairs := p.ReferencePairs()
	out := make([]storage.BindResult, 0, len(pairs))
	for start := 0; start < len(pairs); start += 1000 {
		batch, err := client.Bind(ctx, p.Scope, pairs[start:min(start+1000, len(pairs))])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (s *Service) WithReferences(
	client storage.ReferenceService,
	recheck func(*http.Request) (scope.Identity, error),
) *Service {
	s.references, s.recheck = client, recheck
	return s
}

func changeQuery(tx *db.DB, identity scope.Identity, req *api.CreateVersionRequest) *db.DB {
	return tx.Where(
		"artifact_id = ? AND actor_id = ? AND operation_kind = ? AND request_key = ?",
		req.ArtifactID,
		identity.UserID,
		"create-version",
		req.RequestKey,
	)
}

func unknown() error {
	return serrors.NewError(
		503,
		"OperationOutcomeUnknown",
		"publication outcome cannot be confirmed; retry with the same requestKey and parameters",
	)
}

// prepareReferences checks replay twice: before storage work and inside the durable decision transaction.
func (s *Service) prepareReferences(
	c *core.Context,
	identity scope.Identity,
	req *api.CreateVersionRequest,
) (*table.ReferenceChange, *api.VersionResponse, error) {
	ctx := c.Request.Context()
	var initial table.Artifact
	var n normalized
	var replay *api.VersionResponse
	var found bool
	var files []api.FileReference
	err := db.Transaction(
		ctx,
		s.database,
		db.TransactionOptions{Isolation: sql.LevelReadCommitted},
		func(tx *db.DB) error {
			var e error
			initial, e = findArtifact(tx, identity, req.ArtifactID, true)
			if e != nil {
				return e
			}
			n, e = normalize(req, initial.VersionScheme)
			if e != nil {
				return e
			}
			replay, found, e = readReceipt(tx, initial, identity.UserID, req.RequestKey, n.hash)
			if e != nil || found {
				return e
			}
			var prior table.ReferenceChange
			e = changeQuery(
				tx,
				identity,
				req,
			).Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&prior).
				Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return publicationSource(tx, req, &n, &files)
			}
			if e != nil {
				return e
			}
			if prior.RequestHash != n.hash {
				return serrors.NewError(
					409,
					"IdempotentParameterMismatch",
					"requestKey was used with different parameters",
				)
			}
			now, e := table.DatabaseNow(tx)
			if e != nil {
				return e
			}
			if prior.State == "preparing" && now.Before(prior.Deadline) {
				return serrors.NewError(
					409,
					"RequestInProgress",
					"request is already executing; retry with the same requestKey",
				)
			}
			return publicationSource(tx, req, &n, &files)
		},
	)
	if err != nil {
		if isRevisionConflict(err) {
			err = s.persistSourceConflict(ctx, identity, req, n.hash)
		}
		return nil, nil, databaseError(err)
	}
	if found {
		return nil, replay, nil
	}
	if len(files) == 0 {
		return nil, nil, nil
	}
	if _, err = ValidateFiles(files, s.limits.MaxFilesPerVersion); err != nil {
		return nil, nil, err
	}
	if s.references == nil {
		return nil, nil, serrors.NewError(
			503,
			"StorageUnavailable",
			"storage references are not configured",
		)
	}
	if err = identity.RequireActive(); err != nil {
		return nil, nil, err
	}
	if initial.State != "active" {
		return nil, nil, serrors.NewError(409, "ArtifactArchived", "artifact is archived")
	}
	if int64(len(n.payload)) > s.limits.MaxPayloadBytes {
		return nil, nil, serrors.NewError(413, "PayloadTooLarge", "payload exceeds configured limit")
	}
	if int64(len(n.annotations)) > s.limits.MaxAnnotationsBytes {
		return nil, nil, serrors.NewError(413, "AnnotationsTooLarge", "annotations exceeds configured limit")
	}
	started, err := table.DatabaseNow(s.database.WithContext(ctx))
	if err != nil {
		return nil, nil, databaseError(err)
	}
	deadline := started.Add(120 * time.Second)
	prepareCtx, stopPrepare := context.WithDeadline(ctx, deadline)
	defer stopPrepare()
	target := storage.Scope{TenantID: identity.TenantID, ProjectID: identity.ProjectID}
	var metadata []storage.FileMetadata
	for start := 0; start < len(files); start += 1000 {
		end := min(start+1000, len(files))
		ids := make([]string, end-start)
		for i, f := range files[start:end] {
			ids[i] = f.FileID
		}
		batch, e := s.references.Metadata(prepareCtx, target, ids)
		if e != nil {
			return nil, nil, referenceError(e)
		}
		metadata = append(metadata, batch...)
	}
	for _, m := range metadata {
		if m.Size == nil || (m.State != "ready" && m.State != "bound") {
			return nil, nil, serrors.NewError(409, "FileNotBindable", "file is not ready for publication")
		}
	}
	var change table.ReferenceChange
	completed := false
	err = db.Transaction(
		ctx,
		s.database,
		db.TransactionOptions{Isolation: sql.LevelReadCommitted},
		func(tx *db.DB) error {
			a, e := findArtifact(tx, identity, req.ArtifactID, true)
			if e != nil {
				return e
			}
			result, exists, e := readReceipt(tx, a, identity.UserID, req.RequestKey, n.hash)
			if e != nil {
				return e
			}
			if exists {
				replay = result
				completed = true
				return nil
			}
			if a.State != "active" {
				return serrors.NewError(409, "ArtifactArchived", "artifact is archived")
			}
			var old table.ReferenceChange
			e = changeQuery(tx, identity, req).Clauses(clause.Locking{Strength: "UPDATE"}).First(&old).Error
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			now, e2 := table.DatabaseNow(tx)
			if e2 != nil {
				return e2
			}
			if !now.Before(deadline) {
				return serrors.NewError(409, "RequestAttemptExpired", "preparation deadline elapsed")
			}
			if e == nil {
				if old.RequestHash != n.hash {
					return serrors.NewError(
						409,
						"IdempotentParameterMismatch",
						"requestKey was used with different parameters",
					)
				}
				if old.State == "committed" {
					return unknown()
				}
				if old.State == "preparing" {
					if now.Before(old.Deadline) {
						return serrors.NewError(
							409,
							"RequestInProgress",
							"request is already executing; retry with the same requestKey",
						)
					}
					if e = AbortReferenceChange(tx, &old, now); e != nil {
						return e
					}
				}
			}
			plan := ReferencePlan{Scope: target}
			for i, m := range metadata {
				if m.State == "ready" && !now.Before(m.OriginalExpiresAt) {
					return serrors.NewError(409, "FileNotBindable", "file preparation expired")
				}
				plan.References = append(
					plan.References,
					PlannedReference{
						Reference: storage.Reference{
							ReferenceID: uuid.NewString(),
							FileID:      files[i].FileID,
						},
						OriginalExpiresAt: m.OriginalExpiresAt,
					},
				)
			}
			raw, e := json.Marshal(plan)
			if e != nil {
				return e
			}
			change = table.ReferenceChange{
				ArtifactID:    a.ID,
				ActorID:       identity.UserID,
				OperationKind: "create-version",
				RequestKey:    req.RequestKey,
				RequestHash:   n.hash,
				TenantID:      identity.TenantID,
				ScopeKey:      identity.Key(),
				AttemptID:     uuid.NewString(),
				State:         "preparing",
				Deadline:      deadline,
				Plan:          raw,
			}
			if old.AttemptID == "" {
				e = tx.Create(&change).Error
			} else {
				e = changeQuery(
					tx,
					identity,
					req,
				).Select("attempt_id", "state", "deadline", "plan").
					Updates(&change).
					Error
			}
			if e != nil {
				return e
			}
			completed = true
			return nil
		},
	)
	if err != nil {
		if completed {
			return nil, nil, unknown()
		}
		return nil, nil, databaseError(err)
	}
	if replay != nil {
		return nil, replay, nil
	}
	var plan ReferencePlan
	if err = json.Unmarshal(change.Plan, &plan); err != nil {
		return nil, nil, unknown()
	}
	bindCtx, cancel := context.WithDeadline(ctx, change.Deadline)
	defer cancel()
	bound, err := plan.Bind(bindCtx, s.references)
	if err != nil {
		return nil, nil, s.abortFailure(ctx, identity, req, &change, referenceError(err))
	}
	if len(bound) != len(files) {
		return nil, nil, s.abortFailure(ctx, identity, req, &change, unknown())
	}
	confirmed := publicationPlan{ReferencePlan: plan, Paths: files}
	for _, b := range bound {
		if b.File.Size == nil {
			return nil, nil, s.abortFailure(ctx, identity, req, &change, unknown())
		}
		confirmed.Files = append(confirmed.Files, b.File)
	}
	change.Plan, err = json.Marshal(confirmed)
	if err != nil {
		return nil, nil, s.abortFailure(ctx, identity, req, &change, unknown())
	}
	if s.recheck != nil {
		current, e := s.recheck(c.Request)
		if e != nil {
			return nil, nil, s.abortFailure(ctx, identity, req, &change, e)
		}
		if current.UserID != identity.UserID || current.TenantID != identity.TenantID ||
			current.Key() != identity.Key() {
			return nil, nil, s.abortFailure(
				ctx,
				identity,
				req,
				&change,
				serrors.NewError(403, "ScopeForbidden", "request identity changed"),
			)
		}
		if e = current.RequireActive(); e != nil {
			return nil, nil, s.abortFailure(ctx, identity, req, &change, e)
		}
	}
	return &change, nil, nil
}

func referenceError(err error) error {
	var e *serrors.Error
	if errors.As(err, &e) && e.Status >= 400 && e.Status < 500 {
		return err
	}
	return serrors.NewError(503, "StorageUnavailable", "storage reference service unavailable")
}

// AbortReferenceChange must run under the owning Artifact and ReferenceChange locks.
func AbortReferenceChange(tx *db.DB, change *table.ReferenceChange, now time.Time) error {
	if change.State != "preparing" {
		return nil
	}
	var plan ReferencePlan
	if err := json.Unmarshal(change.Plan, &plan); err != nil {
		return err
	}
	when := now
	for _, ref := range plan.References {
		if ref.OriginalExpiresAt.After(when) {
			when = ref.OriginalExpiresAt
		}
	}
	if len(plan.References) > 0 {
		if err := QueueReferenceRelease(tx, change.AttemptID, plan, when); err != nil {
			return err
		}
	}
	change.State = "aborted"
	return tx.Model(change).Update("state", "aborted").Error
}

func QueueReferenceRelease(tx *db.DB, id string, plan ReferencePlan, when time.Time) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	task := table.BackgroundTask{
		ID:             uuid.NewString(),
		Kind:           "reference-release",
		TargetID:       id,
		Payload:        raw,
		State:          "pending",
		NextRunAt:      when,
		NotBefore:      when,
		Revision:       1,
		RunGeneration:  1,
		RecoveryEvents: []byte("[]"),
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "kind"}, {Name: "target_id"}}, DoNothing: true}).
		Create(&task).
		Error
}

func (s *Service) abortFailure(
	ctx context.Context,
	identity scope.Identity,
	req *api.CreateVersionRequest,
	attempt *table.ReferenceChange,
	cause error,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := db.Transaction(
		cleanup,
		s.database,
		db.TransactionOptions{Isolation: sql.LevelReadCommitted},
		func(tx *db.DB) error {
			if _, e := findArtifact(tx, identity, req.ArtifactID, true); e != nil {
				return e
			}
			var row table.ReferenceChange
			if e := changeQuery(
				tx,
				identity,
				req,
			).Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&row).
				Error; e != nil {
				return e
			}
			if row.AttemptID != attempt.AttemptID || row.State == "aborted" {
				return nil
			}
			if row.State == "committed" {
				return unknown()
			}
			now, e := table.DatabaseNow(tx)
			if e != nil {
				return e
			}
			return AbortReferenceChange(tx, &row, now)
		},
	)
	if err != nil {
		return unknown()
	}
	return cause
}

func commitReferences(
	tx *db.DB,
	identity scope.Identity,
	req *api.CreateVersionRequest,
	attempt *table.ReferenceChange,
	row table.Version,
) error {
	var current table.ReferenceChange
	if err := changeQuery(
		tx,
		identity,
		req,
	).Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&current).
		Error; err != nil {
		return err
	}
	now, err := table.DatabaseNow(tx)
	if err != nil {
		return err
	}
	if current.AttemptID != attempt.AttemptID || current.State != "preparing" || !now.Before(current.Deadline) {
		return serrors.NewError(409, "RequestAttemptExpired", "publication attempt is no longer active")
	}
	var plan publicationPlan
	if err = json.Unmarshal(attempt.Plan, &plan); err != nil {
		return err
	}
	files := make([]table.VersionFile, 0, len(plan.Files))
	for i, m := range plan.Files {
		checksum, e := json.Marshal(m.DeclaredChecksum)
		if e != nil {
			return e
		}
		files = append(
			files,
			table.VersionFile{
				VersionID:        row.ID,
				LogicalPath:      plan.Paths[i].Path,
				FileID:           plan.Paths[i].FileID,
				ReferenceID:      plan.References[i].ReferenceID,
				Size:             *m.Size,
				ContentType:      m.ContentType,
				DeclaredChecksum: checksum,
			},
		)
	}
	if len(files) > 0 {
		if err = tx.CreateInBatches(files, 100).Error; err != nil {
			return err
		}
	}
	return tx.Model(&current).Update("state", "committed").Error
}

// LoadFiles adds the manifest using the caller's transaction snapshot.
func LoadFiles(tx *db.DB, out *api.VersionResponse) error {
	var files []table.VersionFile
	if err := tx.Where("version_id = ?", out.ID).Order("logical_path").Find(&files).Error; err != nil {
		return err
	}
	for _, file := range files {
		var checksum *api.FileChecksum
		if len(file.DeclaredChecksum) > 0 {
			if err := json.Unmarshal(file.DeclaredChecksum, &checksum); err != nil {
				return err
			}
		}
		out.Files = append(
			out.Files,
			api.FileResponse{
				FileID:           file.FileID,
				Path:             file.LogicalPath,
				FileURI:          reference.FileURI(out.VersionURI, file.LogicalPath),
				Size:             file.Size,
				ContentType:      file.ContentType,
				DeclaredChecksum: checksum,
			},
		)
	}
	return nil
}

// publicationPlan contains only this attempt's immutable manifest facts.
type publicationPlan struct {
	ReferencePlan
	Paths []api.FileReference            `json:"paths"`
	Files []storage.ConfirmationMetadata `json:"files"`
}

// publicationSource runs under the Artifact lock shared by draft mutations.
func publicationSource(tx *db.DB, req *api.CreateVersionRequest, n *normalized, files *[]api.FileReference) error {
	*files = n.files
	if req.DraftID == nil {
		return nil
	}
	var draft table.Draft
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("artifact_id = ? AND id = ?", req.ArtifactID, *req.DraftID).
		First(&draft).
		Error; err != nil {
		return err
	}
	if draft.Revision != *req.ExpectedDraftRevision {
		return serrors.NewError(409, "RevisionConflict", "draft revision does not match")
	}
	n.payload, n.annotations = draft.Payload, draft.Annotations
	var rows []table.DraftFile
	if err := tx.Where("draft_id = ?", draft.ID).Order("logical_path").Find(&rows).Error; err != nil {
		return err
	}
	*files = make([]api.FileReference, 0, len(rows))
	for _, row := range rows {
		*files = append(*files, api.FileReference{FileID: row.FileID, Path: row.LogicalPath})
	}
	return nil
}

func isRevisionConflict(err error) bool {
	var app *serrors.Error
	return errors.As(err, &app) && app.Code == "RevisionConflict"
}

// persistSourceConflict pins a rejected source revision even when no Bind was needed.
// It cannot replace another active attempt or a successful publication.
func (s *Service) persistSourceConflict(
	ctx context.Context,
	identity scope.Identity,
	req *api.CreateVersionRequest,
	hash string,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var decision table.ReferenceChange
	completed := false
	err := db.Transaction(
		cleanup,
		s.database,
		db.TransactionOptions{Isolation: sql.LevelReadCommitted},
		func(tx *db.DB) error {
			a, e := findArtifact(tx, identity, req.ArtifactID, true)
			if e != nil {
				return e
			}
			_, found, e := readReceipt(tx, a, identity.UserID, req.RequestKey, hash)
			if e != nil {
				return e
			}
			if found {
				return unknown()
			}
			var prior table.ReferenceChange
			e = changeQuery(
				tx,
				identity,
				req,
			).Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&prior).
				Error
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			now, e2 := table.DatabaseNow(tx)
			if e2 != nil {
				return e2
			}
			if e == nil {
				if prior.RequestHash != hash {
					return serrors.NewError(
						409,
						"IdempotentParameterMismatch",
						"requestKey was used with different parameters",
					)
				}
				if prior.State == "committed" {
					return unknown()
				}
				if prior.State == "preparing" {
					if now.Before(prior.Deadline) {
						return serrors.NewError(
							409,
							"RequestInProgress",
							"request is already executing; retry with the same requestKey",
						)
					}
					if e = AbortReferenceChange(tx, &prior, now); e != nil {
						return e
					}
				}
			}
			raw, e := json.Marshal(
				ReferencePlan{
					Scope: storage.Scope{
						TenantID:  identity.TenantID,
						ProjectID: identity.ProjectID,
					},
				},
			)
			if e != nil {
				return e
			}
			decision = table.ReferenceChange{
				ArtifactID:    req.ArtifactID,
				ActorID:       identity.UserID,
				OperationKind: "create-version",
				RequestKey:    req.RequestKey,
				RequestHash:   hash,
				TenantID:      identity.TenantID,
				ScopeKey:      identity.Key(),
				AttemptID:     uuid.NewString(),
				State:         "aborted",
				Deadline:      now,
				Plan:          raw,
			}
			if prior.AttemptID == "" {
				e = tx.Create(&decision).Error
			} else {
				e = changeQuery(
					tx,
					identity,
					req,
				).Select("attempt_id", "state", "deadline", "plan").
					Updates(&decision).
					Error
			}
			if e != nil {
				return e
			}
			completed = true
			return nil
		},
	)
	conflict := serrors.NewError(409, "RevisionConflict", "draft revision does not match")
	if err == nil {
		return conflict
	}
	if !completed {
		return databaseError(err)
	}
	// A COMMIT error is not a known abort until the authoritative row confirms it.
	var stored table.ReferenceChange
	if e := changeQuery(
		s.database.WithContext(cleanup),
		identity,
		req,
	).First(&stored).
		Error; e == nil && stored.AttemptID == decision.AttemptID && stored.State == "aborted" &&
		stored.RequestHash == hash {
		return conflict
	}
	return unknown()
}
