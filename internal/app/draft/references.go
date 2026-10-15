package draft

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func (s *Service) WithHandoff(
	client *storagehandoff.Client,
	recheck func(*http.Request) (scope.Identity, error),
) *Service {
	s.handoff, s.recheck = client, recheck
	return s
}

type edit struct {
	artifactID, key, kind, draftID string
	expected                       *int64
	source                         *string
	content                        normalized
}

type preparedEdit struct {
	change         table.ReferenceChange
	content        normalized
	files          []table.DraftFile
	removed        []table.DraftFile
	sourceRevision int64
}

func editQuery(tx *db.DB, i scope.Identity, e edit) *db.DB {
	return tx.Where(
		"artifact_id = ? AND actor_id = ? AND operation_kind = ? AND request_key = ?",
		e.artifactID,
		i.UserID,
		e.kind,
		e.key,
	)
}

func inProgress() error {
	return serrors.NewError(409, "RequestInProgress", "request is in progress; retry with the same requestKey")
}

func revisionConflict() error {
	return serrors.NewError(409, "RevisionConflict", "draft revision does not match")
}

func handoffError(err error) error {
	var e *storagehandoff.Error
	if errors.As(err, &e) && !e.OutcomeUnknown && e.Status >= 400 && e.Status < 500 {
		return serrors.NewError(e.Status, e.Code, "storage reference request rejected")
	}
	return serrors.NewError(503, "StorageUnavailable", "storage reference service unavailable")
}

func (s *Service) receipt(tx *db.DB, i scope.Identity, e edit) (*api.DraftResponse, int64, bool, error) {
	if e.kind == "create_draft" {
		out, found, err := createReceipt(tx, e.artifactID, i.UserID, e.key, e.content.hash)
		return out, 0, found, err
	}
	out, found, err := updateReceipt(tx, e.artifactID, i.UserID, e.key, e.content.hash)
	if out == nil {
		return nil, 0, found, err
	}
	return &out.Draft, out.AppliedRevision, found, err
}

// inspect checks the durable decision before any storage request. The artifact
// lock also gives the JSON and attachment source a single consistent snapshot.
func (s *Service) inspect(
	tx *db.DB,
	i scope.Identity,
	e edit,
	p *preparedEdit,
) (*api.DraftResponse, int64, bool, error) {
	a, err := artifact(tx, i, e.artifactID, "UPDATE")
	if err != nil {
		return nil, 0, false, err
	}
	out, rev, found, err := s.receipt(tx, i, e)
	if err != nil || found {
		return out, rev, found, err
	}
	if e.expected != nil {
		var locked table.Draft
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("artifact_id = ? AND id = ?", e.artifactID, e.draftID).
			Find(&locked).
			Error; err != nil {
			return nil, 0, false, err
		}
	}
	if e.source != nil {
		var locked table.Version
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("artifact_id = ? AND id = ?", e.artifactID, *e.source).
			Find(&locked).
			Error; err != nil {
			return nil, 0, false, err
		}
	}
	var prior table.ReferenceChange
	err = editQuery(tx, i, e).Clauses(clause.Locking{Strength: "UPDATE"}).First(&prior).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, false, err
	}
	now, err2 := table.DatabaseNow(tx)
	if err2 != nil {
		return nil, 0, false, err2
	}
	if err == nil {
		if prior.RequestHash != e.content.hash {
			return nil, 0, false, mismatch()
		}
		if prior.State == "preparing" {
			if now.Before(prior.Deadline) {
				return nil, 0, false, inProgress()
			}
			if err = version.AbortReferenceChange(tx, &prior, now); err != nil {
				return nil, 0, false, err
			}
		}
	}
	if err = active(i, a); err != nil {
		return nil, 0, false, err
	}
	p.content = e.content
	p.files = nil
	p.removed = nil
	if e.source != nil {
		var v table.Version
		if err = tx.Where("artifact_id = ? AND id = ? AND state = ?", a.ID, *e.source, "ready").
			First(&v).
			Error; err != nil {
			return nil, 0, false, err
		}
		p.sourceRevision = v.Revision
		p.content.payload = v.Payload
		p.content.annotations = v.Annotations
		var files []table.VersionFile
		if err = tx.Where("version_id = ?", v.ID).Order("logical_path").Find(&files).Error; err != nil {
			return nil, 0, false, err
		}
		p.content.files = nil
		for _, f := range files {
			p.content.files = append(
				p.content.files,
				api.FileReference{FileID: f.FileID, Path: f.LogicalPath},
			)
		}
	}
	if err = s.size(p.content); err != nil {
		return nil, 0, false, err
	}
	if len(p.content.files) > s.limits.MaxFilesPerVersion {
		return nil, 0, false, invalid("file count exceeds configured limit")
	}
	existing := map[string]table.DraftFile{}
	if e.expected != nil {
		d, readErr := readDraft(tx, a.ID, e.draftID, "UPDATE")
		if readErr != nil {
			return nil, 0, false, readErr
		}
		if d.Revision != *e.expected {
			return nil, 0, false, revisionConflict()
		}
		if d.Revision == math.MaxInt64 {
			return nil, 0, false, serrors.NewError(409, "RevisionExhausted", "draft revision exhausted")
		}
		var files []table.DraftFile
		if err = tx.Where("draft_id = ?", d.ID).Find(&files).Error; err != nil {
			return nil, 0, false, err
		}
		for _, f := range files {
			existing[f.FileID] = f
		}
	}
	for _, f := range p.content.files {
		row, exists := existing[f.FileID]
		if exists {
			delete(existing, f.FileID)
		} else {
			row = table.DraftFile{FileID: f.FileID}
		}
		row.LogicalPath = f.Path
		p.files = append(p.files, row)
	}
	for _, f := range existing {
		p.removed = append(p.removed, f)
	}
	return nil, 0, false, nil
}

func (s *Service) apply(
	c *core.Context,
	i scope.Identity,
	e edit,
) (out *api.DraftResponse, applied int64, replay bool, result error) {
	ctx := c.Request.Context()
	var p preparedEdit
	var rejected error
	localCompleted := false
	inspected := false
	err := db.Transaction(ctx, s.database, options(), func(tx *db.DB) error {
		localCompleted = false
		inspected = false
		rejected = nil
		var err error
		out, applied, replay, err = s.inspect(tx, i, e, &p)
		if err != nil {
			var problem *serrors.Error
			if errors.As(err, &problem) && problem.Code == "RevisionConflict" {
				now, e2 := table.DatabaseNow(tx)
				if e2 != nil {
					return e2
				}
				p.change = newChange(i, e, e.draftID, now)
				p.change.State = "aborted"
				if e2 = saveChange(tx, &p.change); e2 != nil {
					return e2
				}
				rejected = err
				localCompleted = true
				return nil
			}
			return err
		}
		if replay {
			localCompleted = true
			return nil
		}
		for _, f := range p.files {
			if f.ReferenceID == "" {
				inspected = true
				return nil
			}
		}
		if e.expected == nil {
			p.change.DraftID = uuid.NewString()
		} else {
			p.change.DraftID = e.draftID
		}
		if err = s.finish(tx, i, e, &p, true, &out, &applied); err != nil {
			return err
		}
		localCompleted = true
		return nil
	})
	if err != nil {
		if localCompleted {
			err = s.confirm(ctx, i, e.artifactID, func(tx *db.DB) error {
				if rejected != nil {
					var row table.ReferenceChange
					if e2 := editQuery(tx, i, e).First(&row).Error; e2 != nil {
						return unknown()
					}
					if row.AttemptID == p.change.AttemptID && row.State == "aborted" {
						return rejected
					}
					return unknown()
				}
				var found bool
				var e2 error
				out, applied, found, e2 = s.receipt(tx, i, e)
				if e2 == nil && !found {
					return unknown()
				}
				return e2
			})
			return out, applied, true, err
		}
		if inspected {
			return nil, 0, false, unknown()
		}
		return nil, 0, false, databaseError(err)
	}
	if localCompleted {
		return out, applied, replay, rejected
	}
	started, err := table.DatabaseNow(s.database.WithContext(ctx))
	if err != nil {
		return nil, 0, false, databaseError(err)
	}
	deadline := started.Add(120 * time.Second)
	attemptCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	target := storagehandoff.Scope{TenantID: i.TenantID, ProjectID: i.ProjectID}
	ids := []string{}
	for _, f := range p.files {
		if f.ReferenceID == "" {
			ids = append(ids, f.FileID)
		}
	}
	metadata := map[string]storagehandoff.FileMetadata{}
	if len(ids) > 0 && s.handoff == nil {
		return nil, 0, false, serrors.NewError(
			503,
			"StorageUnavailable",
			"storage reference handoff is not configured",
		)
	}
	for start := 0; start < len(ids); start += 1000 {
		batch, err := s.handoff.Metadata(attemptCtx, target, ids[start:min(start+1000, len(ids))])
		if err != nil {
			return nil, 0, false, handoffError(err)
		}
		for _, f := range batch {
			metadata[f.FileID] = f
		}
	}
	completed := false
	err = db.Transaction(attemptCtx, s.database, options(), func(tx *db.DB) (decisionErr error) {
		completed = false
		rejected = nil
		defer func() {
			var problem *serrors.Error
			if errors.As(decisionErr, &problem) && problem.Code == "RevisionConflict" {
				now, e2 := table.DatabaseNow(tx)
				if e2 != nil {
					decisionErr = e2
					return
				}
				p.change = newChange(i, e, e.draftID, now)
				p.change.State = "aborted"
				if e2 = saveChange(tx, &p.change); e2 != nil {
					decisionErr = e2
					return
				}
				rejected = decisionErr
				decisionErr = nil
				completed = true
			}
		}()
		var current preparedEdit
		var err error
		out, applied, replay, err = s.inspect(tx, i, e, &current)
		if err != nil {
			return err
		}
		if replay {
			completed = true
			return nil
		}
		// A source snapshot cannot silently change while external preparation runs.
		if e.source != nil && current.sourceRevision != p.sourceRevision {
			return revisionConflict()
		}
		p = current
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		if !now.Before(deadline) {
			return serrors.NewError(409, "RequestAttemptExpired", "preparation deadline elapsed")
		}
		plan := version.ReferencePlan{Scope: target}
		for idx := range p.files {
			f := &p.files[idx]
			if f.ReferenceID != "" {
				continue
			}
			m, ok := metadata[f.FileID]
			if !ok || m.Size == nil || (m.State != "ready" && m.State != "bound") ||
				(m.State == "ready" && !now.Before(m.OriginalExpiresAt)) {
				return serrors.NewError(409, "FileNotBindable", "file is not ready for binding")
			}
			f.ReferenceID = uuid.NewString()
			plan.References = append(
				plan.References,
				version.PlannedReference{
					Reference: storagehandoff.Reference{
						ReferenceID: f.ReferenceID,
						FileID:      f.FileID,
					},
					OriginalExpiresAt: m.OriginalExpiresAt,
				},
			)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		draftID := e.draftID
		if e.expected == nil {
			draftID = uuid.NewString()
		}
		p.change = table.ReferenceChange{
			ArtifactID:    e.artifactID,
			ActorID:       i.UserID,
			OperationKind: e.kind,
			RequestKey:    e.key,
			RequestHash:   e.content.hash,
			TenantID:      i.TenantID,
			ScopeKey:      i.Key(),
			AttemptID:     uuid.NewString(),
			State:         "preparing",
			Deadline:      deadline,
			Plan:          raw,
			DraftID:       draftID,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err = saveChange(tx, &p.change); err != nil {
			return err
		}
		completed = true
		return nil
	})
	if err != nil {
		if completed {
			if rejected != nil {
				err = s.confirm(ctx, i, e.artifactID, func(tx *db.DB) error {
					var row table.ReferenceChange
					if e2 := editQuery(
						tx,
						i,
						e,
					).First(&row).
						Error; e2 == nil && row.AttemptID == p.change.AttemptID &&
						row.State == "aborted" {
						return rejected
					}
					return unknown()
				})
				return nil, 0, false, err
			}
			return nil, 0, false, unknown()
		}
		return nil, 0, false, databaseError(err)
	}
	if rejected != nil {
		return nil, 0, false, rejected
	}
	if replay {
		return out, applied, true, nil
	}
	var plan version.ReferencePlan
	if json.Unmarshal(p.change.Plan, &plan) != nil {
		return nil, 0, false, unknown()
	}
	bound, err := plan.Bind(attemptCtx, s.handoff)
	if err != nil {
		return nil, 0, false, s.abort(ctx, i, e, p.change, handoffError(err))
	}
	confirmed := map[string]storagehandoff.ConfirmationMetadata{}
	for _, f := range bound {
		confirmed[f.ReferenceID] = f.File
	}
	for idx := range p.files {
		f := &p.files[idx]
		if m, ok := confirmed[f.ReferenceID]; ok {
			if m.Size == nil {
				return nil, 0, false, s.abort(ctx, i, e, p.change, unknown())
			}
			f.Size = *m.Size
			f.ContentType = m.ContentType
			f.DeclaredChecksum, _ = json.Marshal(m.DeclaredChecksum)
		}
	}
	if s.recheck != nil {
		current, err := s.recheck(c.Request)
		if err == nil &&
			(current.UserID != i.UserID || current.TenantID != i.TenantID || current.Key() != i.Key()) {
			err = serrors.NewError(403, "ScopeForbidden", "request identity changed")
		}
		if err == nil {
			err = current.RequireActive()
		}
		if err != nil {
			return nil, 0, false, s.abort(ctx, i, e, p.change, err)
		}
	}
	completed = false
	err = db.Transaction(attemptCtx, s.database, options(), func(tx *db.DB) error {
		completed = false
		if err := s.finish(tx, i, e, &p, false, &out, &applied); err != nil {
			return err
		}
		completed = true
		return nil
	})
	if err != nil {
		if completed {
			err = s.confirm(ctx, i, e.artifactID, func(tx *db.DB) error {
				var found bool
				var err error
				out, applied, found, err = s.receipt(tx, i, e)
				if err == nil && !found {
					return unknown()
				}
				return err
			})
			return out, applied, true, err
		}
		return nil, 0, false, s.abort(ctx, i, e, p.change, databaseError(err))
	}
	return out, applied, false, nil
}

func saveChange(tx *db.DB, p *table.ReferenceChange) error {
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "artifact_id"}, {Name: "actor_id"}, {Name: "operation_kind"}, {Name: "request_key"}}, DoUpdates: clause.AssignmentColumns([]string{"attempt_id", "state", "deadline", "plan", "draft_id", "applied_revision", "updated_at"})}).
		Create(p).
		Error
}

func (s *Service) abort(
	ctx context.Context,
	i scope.Identity,
	e edit,
	attempt table.ReferenceChange,
	cause error,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := db.Transaction(cleanup, s.database, options(), func(tx *db.DB) error {
		if _, err := artifact(tx, i, e.artifactID, "UPDATE"); err != nil {
			return err
		}
		var current table.ReferenceChange
		if err := editQuery(
			tx,
			i,
			e,
		).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&current).
			Error; err != nil {
			return err
		}
		if current.AttemptID != attempt.AttemptID || current.State == "aborted" {
			return nil
		}
		if current.State == "committed" {
			return unknown()
		}
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		return version.AbortReferenceChange(tx, &current, now)
	})
	if err != nil {
		return unknown()
	}
	return cause
}

func (s *Service) Create(c *core.Context, req *api.CreateDraftRequest) (*api.DraftResponse, bool, error) {
	i, err := scope.From(c)
	if err != nil {
		return nil, false, err
	}
	n, err := normalize(req.RequestKey, req.Payload, req.Annotations, req.Files, req.SourceVersionID, "", nil)
	if err != nil {
		return nil, false, err
	}
	out, _, replay, err := s.apply(
		c,
		i,
		edit{
			artifactID: req.ArtifactID,
			key:        req.RequestKey,
			kind:       "create_draft",
			source:     req.SourceVersionID,
			content:    n,
		},
	)
	return out, replay, err
}

func (s *Service) Update(c *core.Context, req *api.UpdateDraftRequest) (*api.UpdateDraftResponse, error) {
	i, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision == nil || *req.ExpectedRevision <= 0 || len(req.Payload) == 0 ||
		len(req.Annotations) == 0 ||
		req.Files == nil {
		return nil, invalid(
			"expectedRevision must be positive and payload, annotations and files must be provided",
		)
	}
	n, err := normalize(
		req.RequestKey,
		req.Payload,
		req.Annotations,
		req.Files,
		nil,
		req.DraftID,
		req.ExpectedRevision,
	)
	if err != nil {
		return nil, err
	}
	out, applied, _, err := s.apply(
		c,
		i,
		edit{
			artifactID: req.ArtifactID,
			key:        req.RequestKey,
			kind:       "update_draft",
			draftID:    req.DraftID,
			expected:   req.ExpectedRevision,
			content:    n,
		},
	)
	if err != nil {
		return nil, err
	}
	return &api.UpdateDraftResponse{AppliedRevision: applied, Draft: *out}, nil
}

func (s *Service) finish(
	tx *db.DB,
	i scope.Identity,
	e edit,
	p *preparedEdit,
	local bool,
	out **api.DraftResponse,
	applied *int64,
) error {
	a, err := artifact(tx, i, e.artifactID, "UPDATE")
	if err != nil {
		return err
	}
	if err = active(i, a); err != nil {
		return err
	}
	now, err := table.DatabaseNow(tx)
	if err != nil {
		return err
	}
	var old table.Draft
	if e.expected != nil {
		old, err = readDraft(tx, a.ID, e.draftID, "UPDATE")
		if err != nil {
			return err
		}
		if old.Revision != *e.expected {
			return revisionConflict()
		}
	}
	if e.source != nil {
		var v table.Version
		if err = tx.Where("artifact_id = ? AND id = ? AND state = ?", a.ID, *e.source, "ready").
			First(&v).
			Error; err != nil {
			return err
		}
		if v.Revision != p.sourceRevision {
			return revisionConflict()
		}
	}
	var current table.ReferenceChange
	if !local {
		if err = editQuery(
			tx,
			i,
			e,
		).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&current).
			Error; err != nil {
			return err
		}
		if current.AttemptID != p.change.AttemptID || current.State != "preparing" ||
			!now.Before(current.Deadline) {
			return serrors.NewError(409, "RequestAttemptExpired", "draft attempt is no longer active")
		}
	}
	d := table.Draft{
		ID:          p.change.DraftID,
		ArtifactID:  a.ID,
		Revision:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
		Payload:     p.content.payload,
		Annotations: p.content.annotations,
	}
	if e.expected != nil {
		d.Revision = old.Revision + 1
		d.CreatedAt = old.CreatedAt
		if err = tx.Model(&table.Draft{}).
			Where("id = ?", d.ID).
			Updates(map[string]any{"payload": d.Payload, "annotations": d.Annotations, "revision": d.Revision, "updated_at": now}).
			Error; err != nil {
			return err
		}
		if err = tx.Where("draft_id = ?", d.ID).Delete(&table.DraftFile{}).Error; err != nil {
			return err
		}
	} else {
		if err = tx.Create(&d).Error; err != nil {
			return err
		}
		r := table.DraftCreateRequest{
			ArtifactID:  a.ID,
			ActorID:     i.UserID,
			RequestKey:  e.key,
			RequestHash: e.content.hash,
			DraftID:     d.ID,
			CreatedAt:   now,
		}
		if err = tx.Create(&r).Error; err != nil {
			return err
		}
	}
	for idx := range p.files {
		p.files[idx].DraftID = d.ID
	}
	if len(p.files) > 0 {
		if err = tx.CreateInBatches(&p.files, 100).Error; err != nil {
			return err
		}
	}
	release := version.ReferencePlan{Scope: storagehandoff.Scope{TenantID: i.TenantID, ProjectID: i.ProjectID}}
	for _, f := range p.removed {
		release.References = append(
			release.References,
			version.PlannedReference{
				Reference: storagehandoff.Reference{ReferenceID: f.ReferenceID, FileID: f.FileID},
			},
		)
	}
	if len(release.References) > 0 {
		if err = version.QueueReferenceRelease(tx, uuid.NewString(), release, now); err != nil {
			return err
		}
	}
	if local {
		p.change = newChange(i, e, d.ID, now)
		p.change.State = "committed"
		p.change.AppliedRevision = &d.Revision
		if err = saveChange(tx, &p.change); err != nil {
			return err
		}
	} else {
		if err = tx.Model(&current).
			Updates(map[string]any{"state": "committed", "applied_revision": d.Revision, "updated_at": now}).
			Error; err != nil {
			return err
		}
	}
	value, err := response(tx, d)
	if err != nil {
		return err
	}
	*out = &value
	*applied = d.Revision
	return nil
}

func newChange(i scope.Identity, e edit, draftID string, now time.Time) table.ReferenceChange {
	return table.ReferenceChange{
		ArtifactID:    e.artifactID,
		ActorID:       i.UserID,
		OperationKind: e.kind,
		RequestKey:    e.key,
		RequestHash:   e.content.hash,
		TenantID:      i.TenantID,
		ScopeKey:      i.Key(),
		AttemptID:     uuid.NewString(),
		State:         "preparing",
		Deadline:      now,
		Plan:          []byte(`[]`),
		DraftID:       draftID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}
