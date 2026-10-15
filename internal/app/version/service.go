package version

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/app/alias"
	"github.com/FlyTOmeLight/artifact-go/internal/app/retention"
	"github.com/FlyTOmeLight/artifact-go/internal/cursor"
	"github.com/FlyTOmeLight/artifact-go/internal/reference"
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

func invalid(message string) error { return serrors.NewError(400, "InvalidVersionRequest", message) }

func exhausted() error {
	return serrors.NewError(409, "VersionNumberExhausted", "version counter exhausted")
}

func databaseError(err error) error {
	var deleted *deletedError
	if errors.As(err, &deleted) {
		return err
	}
	var appErr *serrors.Error
	if errors.As(err, &appErr) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return serrors.NewError(404, "ResourceNotFound", "resource not found")
	}
	return serrors.ErrorInternalError
}

func findArtifact(tx *db.DB, identity scope.Identity, id string, lock bool) (table.Artifact, error) {
	var a table.Artifact
	q := tx.Where("tenant_id = ? AND scope_key = ?", identity.TenantID, identity.Key())
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	started := time.Now()
	err := q.First(&a, "id = ?", id).Error
	if lock {
		telemetry.ObserveArtifactLock("version", time.Since(started))
	}
	return a, err
}

type deletedError struct{ identity api.VersionIdentity }

func (e *deletedError) Error() string { return "version is deleted" }

func (s *Service) Create(c *core.Context, req *api.CreateVersionRequest) (*api.VersionResponse, bool, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, false, err
	}
	var attempt *table.ReferenceChange
	if len(req.Files) > 0 || req.DraftID != nil {
		var replay *api.VersionResponse
		attempt, replay, err = s.prepareReferences(c, identity, req)
		if err != nil {
			return nil, false, err
		}
		if replay != nil {
			return replay, true, nil
		}
	}
	var result api.VersionResponse
	replayed := false
	callbackCompleted := false
	var requestHash string
	err = db.Transaction(c.Request.Context(), s.database, db.TransactionOptions{
		Isolation: sql.LevelReadCommitted,
		Retry:     &db.TransactionRetry{MaxAttempts: 3, Timeout: 5 * time.Second, Delay: 10 * time.Millisecond},
	}, func(tx *db.DB) error {
		callbackCompleted = false
		replayed = false
		a, err := findArtifact(tx, identity, req.ArtifactID, true)
		if err != nil {
			return err
		}
		n, err := normalize(req, a.VersionScheme)
		if err != nil {
			return err
		}
		requestHash = n.hash
		existing, found, err := readReceipt(tx, a, identity.UserID, req.RequestKey, n.hash)
		if err != nil {
			return err
		}
		if found {
			result = *existing
			replayed = true
			callbackCompleted = true
			return nil
		}
		if attempt == nil {
			var prior table.ReferenceChange
			priorErr := changeQuery(
				tx,
				identity,
				req,
			).Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&prior).
				Error
			if priorErr != nil && !errors.Is(priorErr, gorm.ErrRecordNotFound) {
				return priorErr
			}
			if priorErr == nil && prior.RequestHash != n.hash {
				return serrors.NewError(
					409,
					"IdempotentParameterMismatch",
					"requestKey was used with different parameters",
				)
			}
		}
		if err = identity.RequireActive(); err != nil {
			return err
		}
		if a.State != "active" {
			return serrors.NewError(409, "ArtifactArchived", "artifact is archived")
		}
		if req.DraftID != nil {
			var files []api.FileReference
			if err = publicationSource(tx, req, &n, &files); err != nil {
				return err
			}
			n.files = files
		}

		if _, err = ValidateFiles(n.files, s.limits.MaxFilesPerVersion); err != nil {
			return err
		}
		if int64(len(n.payload)) > s.limits.MaxPayloadBytes {
			return serrors.NewError(413, "PayloadTooLarge", "payload exceeds configured limit")
		}
		if int64(len(n.annotations)) > s.limits.MaxAnnotationsBytes {
			return serrors.NewError(413, "AnnotationsTooLarge", "annotations exceeds configured limit")
		}
		preparedAliases := make([]*table.Alias, 0, len(n.aliases))
		for _, update := range n.aliases {
			prepared, err := alias.PreparePut(tx, a.ID, update.Name, *update.ExpectedRevision)
			if err != nil {
				return err
			}
			preparedAliases = append(preparedAliases, prepared)
		}
		next, err := NextNumber(a, req.Bump)
		if err != nil {
			return err
		}
		now, err := table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		row := table.Version{
			ID:          uuid.NewString(),
			ArtifactID:  a.ID,
			Sequence:    next.Sequence,
			Number:      next.Value,
			Major:       next.Major,
			Minor:       next.Minor,
			Patch:       next.Patch,
			Payload:     n.payload,
			Annotations: n.annotations,
			Revision:    1,
			Protected:   req.Protected,
			State:       "ready",
			CreatedBy:   identity.UserID,
			PublishedAt: now,
		}
		policy, err := retention.Load(tx, a.ID)
		if err != nil {
			return err
		}
		facts, err := retention.LoadFacts(tx, a.ID)
		if err != nil {
			return err
		}
		// Plan observes the proposed final references: aliases moved to the new
		// version must not keep protecting their previous targets.
		var refs []table.Alias
		if err = tx.Where("artifact_id = ? AND version_id IS NOT NULL", a.ID).Find(&refs).Error; err != nil {
			return err
		}
		moved := make(map[string]bool, len(preparedAliases))
		for _, item := range preparedAliases {
			moved[item.Name] = true
		}
		targets := make(map[string]bool, len(refs))
		for _, ref := range refs {
			if !moved[ref.Name] {
				targets[*ref.VersionID] = true
			}
		}
		for i := range facts {
			facts[i].Aliased = targets[facts[i].ID]
		}
		facts = append(
			facts,
			retention.VersionFact{
				ID:          row.ID,
				Sequence:    row.Sequence,
				PublishedAt: now,
				Protected:   row.Protected,
				Aliased:     len(preparedAliases) > 0,
			},
		)
		plan := retention.Plan(
			retention.PlanInput{
				Policy:       policy,
				Versions:     facts,
				Now:          now,
				Publishing:   true,
				NewVersionID: row.ID,
			},
		)
		if plan.Blocked {
			return serrors.NewError(409, "RetentionBlocked", "retention policy prevents publication")
		}
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		if attempt != nil {
			if err = commitReferences(tx, identity, req, attempt, row); err != nil {
				return err
			}
		}
		for _, prepared := range preparedAliases {
			if _, err = alias.ApplyPut(tx, prepared, row.ID); err != nil {
				return err
			}
		}
		if err = applyCandidates(tx, a.ID, plan.Candidates, now); err != nil {
			return err
		}
		updates := map[string]any{"last_sequence": next.Sequence, "last_published_at": now}
		if policy.Enabled {
			updates["next_retention_at"] = now
		}

		if next.Major != nil {
			updates["last_major"] = *next.Major
			updates["last_minor"] = *next.Minor
			updates["last_patch"] = *next.Patch
		}
		if err = tx.Model(&table.Artifact{}).Where("id = ?", a.ID).Updates(updates).Error; err != nil {
			return err
		}
		receipt := table.CreateRequest{
			ArtifactID:  a.ID,
			ActorID:     identity.UserID,
			RequestKey:  req.RequestKey,
			RequestHash: n.hash,
			VersionID:   row.ID,
			CreatedAt:   now,
		}
		if err = tx.Create(&receipt).Error; err != nil {
			return err
		}
		result = Response(a, row)
		if err = LoadFiles(tx, &result); err != nil {
			return err
		}
		callbackCompleted = true
		return nil
	})
	if err != nil {
		if callbackCompleted {
			return s.confirmOutcome(c.Request.Context(), identity, req, requestHash)
		}
		var deleted *deletedError
		if errors.As(err, &deleted) {
			return nil, false, err
		}
		if attempt != nil {
			return nil, false, s.abortFailure(
				c.Request.Context(),
				identity,
				req,
				attempt,
				databaseError(err),
			)
		}
		if req.DraftID != nil && isRevisionConflict(err) {
			err = s.persistSourceConflict(c.Request.Context(), identity, req, requestHash)
		}
		return nil, false, databaseError(err)
	}
	return &result, replayed, nil
}

// readReceipt is shared by lock-protected publication and authoritative outcome
// confirmation. A missing receipt permits a new publication only in the former.
func readReceipt(
	database *db.DB,
	a table.Artifact,
	actorID, requestKey, hash string,
) (*api.VersionResponse, bool, error) {
	var receipt table.CreateRequest
	err := database.Where("artifact_id = ? AND actor_id = ? AND request_key = ?", a.ID, actorID, requestKey).
		First(&receipt).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if receipt.RequestHash != hash {
		return nil, true, serrors.NewError(
			409,
			"IdempotentParameterMismatch",
			"requestKey was used with different parameters",
		)
	}
	var row table.Version
	if err = database.Where("artifact_id = ? AND id = ?", a.ID, receipt.VersionID).First(&row).Error; err != nil {
		return nil, true, err
	}
	if row.State != "ready" {
		return nil, true, &deletedError{identity: versionIdentity(a, row)}
	}
	result := Response(a, row)
	if err = LoadFiles(database, &result); err != nil {
		return nil, true, err
	}
	return &result, true, nil
}

func (s *Service) confirmOutcome(
	ctx context.Context,
	identity scope.Identity,
	req *api.CreateVersionRequest,
	hash string,
) (*api.VersionResponse, bool, error) {
	// A client disconnect cannot establish whether COMMIT succeeded. This bounded,
	// synchronous read outlives request cancellation only to confirm its receipt;
	// it performs no writes and is joined before the handler returns.
	confirmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var result *api.VersionResponse
	var found bool
	err := db.Transaction(
		confirmCtx,
		s.database,
		db.TransactionOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true},
		func(tx *db.DB) error {
			a, e := findArtifact(tx, identity, req.ArtifactID, false)
			if e != nil {
				return e
			}
			result, found, e = readReceipt(tx, a, identity.UserID, req.RequestKey, hash)
			return e
		},
	)
	if err == nil && found {
		return result, true, nil
	}
	var deleted *deletedError
	if errors.As(err, &deleted) {
		return nil, true, err
	}
	var problem *serrors.Error
	if errors.As(err, &problem) && problem.Code == "IdempotentParameterMismatch" {
		return nil, false, err
	}
	return nil, false, serrors.NewError(
		503,
		"OperationOutcomeUnknown",
		"publication outcome cannot be confirmed; retry with the same requestKey and parameters",
	)
}

func (s *Service) List(c *core.Context, req *api.ListVersionsRequest) (*api.ListVersionsResponse, error) {
	var out *api.ListVersionsResponse
	err := db.Transaction(
		c.Request.Context(),
		s.database,
		db.TransactionOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true},
		func(tx *db.DB) error {
			snapshot := *s
			snapshot.database = tx
			var err error
			out, err = snapshot.list(c, req)
			return err
		},
	)
	if err != nil {
		return nil, databaseError(err)
	}
	return out, nil
}

func (s *Service) list(c *core.Context, req *api.ListVersionsRequest) (*api.ListVersionsResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	a, err := findArtifact(s.database.WithContext(c.Request.Context()), identity, req.ArtifactID, false)
	if err != nil {
		return nil, databaseError(err)
	}
	query := cursor.Query{
		TenantID:   identity.TenantID,
		ScopeKey:   identity.Key(),
		ArtifactID: a.ID,
		Number:     req.Number,
		Order:      "sequence-desc",
	}
	after, err := cursor.Decode(req.Cursor, query)
	if err != nil {
		return nil, serrors.NewError(400, "InvalidCursor", err.Error())
	}
	q := s.database.WithContext(c.Request.Context()).Where("artifact_id = ? AND state = ?", a.ID, "ready")
	if req.Number != "" {
		q = q.Where("number = ?", req.Number)
	}
	if after != "" {
		value, _ := strconv.ParseInt(after, 10, 64)
		q = q.Where("sequence < ?", value)
	}
	var rows []table.Version
	if err = q.Order("sequence DESC").Limit(req.Limit + 1).Find(&rows).Error; err != nil {
		return nil, databaseError(err)
	}
	out := api.ListVersionsResponse{Items: make([]api.VersionResponse, 0, len(rows))}
	if len(rows) > req.Limit {
		rows = rows[:req.Limit]
		out.NextCursor = cursor.Encode(query, strconv.FormatInt(rows[len(rows)-1].Sequence, 10))
	}
	for _, row := range rows {
		item := Response(a, row)
		if err = LoadFiles(s.database.WithContext(c.Request.Context()), &item); err != nil {
			return nil, databaseError(err)
		}
		out.Items = append(out.Items, item)
	}
	return &out, nil
}

func (s *Service) Get(c *core.Context, req *api.GetVersionRequest) (*api.VersionResponse, error) {
	var out *api.VersionResponse
	err := db.Transaction(
		c.Request.Context(),
		s.database,
		db.TransactionOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true},
		func(tx *db.DB) error {
			snapshot := *s
			snapshot.database = tx
			var err error
			out, err = snapshot.get(c, req)
			return err
		},
	)
	if err != nil {
		return nil, databaseError(err)
	}
	return out, nil
}

func (s *Service) get(c *core.Context, req *api.GetVersionRequest) (*api.VersionResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	a, err := findArtifact(s.database.WithContext(c.Request.Context()), identity, req.ArtifactID, false)
	if err != nil {
		return nil, databaseError(err)
	}
	var row table.Version
	if err = s.database.WithContext(c.Request.Context()).
		Where("artifact_id = ? AND id = ? AND state = ?", a.ID, req.VersionID, "ready").
		First(&row).
		Error; err != nil {
		return nil, databaseError(err)
	}
	out := Response(a, row)
	if err = LoadFiles(s.database.WithContext(c.Request.Context()), &out); err != nil {
		return nil, databaseError(err)
	}
	return &out, nil
}

func versionIdentity(a table.Artifact, row table.Version) api.VersionIdentity {
	return api.VersionIdentity{
		ID:         row.ID,
		ArtifactID: a.ID,
		Version:    row.Number,
		Sequence:   row.Sequence,
		VersionURI: reference.VersionURI(a.TenantID, a.ProjectID, a.Type, a.ID, row.ID),
	}
}

// Response maps persisted business metadata to its public representation.
func Response(a table.Artifact, row table.Version) api.VersionResponse {
	return api.VersionResponse{
		VersionIdentity: versionIdentity(a, row),
		Payload:         row.Payload,
		Annotations:     row.Annotations,
		Protected:       row.Protected,
		Revision:        row.Revision,
		State:           api.VersionState(row.State),
		Files:           []api.FileResponse{},
		CreatedBy:       row.CreatedBy,
		PublishedAt:     row.PublishedAt.UTC(),
	}
}

func (s *Service) UpdateAnnotations(c *core.Context, req *api.UpdateAnnotationsRequest) (*api.VersionResponse, error) {
	if len(req.Annotations) == 0 {
		return nil, invalid("annotations must be provided")
	}
	value, _, err := object(req.Annotations)
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > s.limits.MaxAnnotationsBytes {
		return nil, serrors.NewError(413, "AnnotationsTooLarge", "annotations exceeds configured limit")
	}
	return s.updateMetadata(c, req.VersionPath, req.ExpectedRevision, map[string]any{"annotations": []byte(value)})
}

func (s *Service) UpdateProtection(c *core.Context, req *api.UpdateProtectionRequest) (*api.VersionResponse, error) {
	if req.Protected == nil {
		return nil, invalid("protected must be provided as a boolean")
	}
	return s.updateMetadata(c, req.VersionPath, req.ExpectedRevision, map[string]any{"protected": *req.Protected})
}

// Both management fields share one revision and the publication lock. Historical
// ownership and archived collections remain maintainable; only ready versions are editable.
func (s *Service) updateMetadata(
	c *core.Context,
	path api.VersionPath,
	expected *int64,
	updates map[string]any,
) (*api.VersionResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	if expected == nil || *expected < 0 {
		return nil, invalid("expectedRevision must be provided and non-negative")
	}
	var result api.VersionResponse
	callbackCompleted := false
	err = db.Transaction(c.Request.Context(), s.database, db.TransactionOptions{
		Isolation: sql.LevelReadCommitted,
		Retry:     &db.TransactionRetry{MaxAttempts: 3, Timeout: 5 * time.Second, Delay: 10 * time.Millisecond},
	}, func(tx *db.DB) error {
		callbackCompleted = false
		a, err := findArtifact(tx, identity, path.ArtifactID, true)
		if err != nil {
			return err
		}
		var row table.Version
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("artifact_id = ? AND id = ? AND state = ?", a.ID, path.VersionID, "ready").
			First(&row).
			Error; err != nil {
			return err
		}
		if row.Revision != *expected {
			return serrors.NewError(409, "RevisionConflict", "version revision does not match")
		}
		if row.Revision == math.MaxInt64 {
			return serrors.NewError(409, "RevisionExhausted", "version revision exhausted")
		}
		updates["revision"] = row.Revision + 1
		if err = tx.Model(&table.Version{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			return err
		}
		row.Revision++
		if value, ok := updates["annotations"]; ok {
			row.Annotations = value.([]byte)
		}
		if value, ok := updates["protected"]; ok {
			row.Protected = value.(bool)
		}
		result = Response(a, row)
		if err = LoadFiles(tx, &result); err != nil {
			return err
		}
		callbackCompleted = true
		return nil
	})
	if err != nil {
		if callbackCompleted {
			return nil, serrors.NewError(
				503,
				"OperationOutcomeUnknown",
				"management update outcome cannot be confirmed; read the current version revision before retrying",
			)
		}
		return nil, databaseError(err)
	}
	return &result, nil
}
