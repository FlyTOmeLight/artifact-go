package alias

import (
	"database/sql"
	"errors"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/reference"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type Service struct {
	database            *db.DB
	maxRequestBodyBytes int64
}

func NewService(database *db.DB, maxRequestBodyBytes int64) *Service {
	return &Service{database: database, maxRequestBodyBytes: maxRequestBodyBytes}
}

func databaseError(err error) error {
	var appErr *serrors.Error
	if errors.As(err, &appErr) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return serrors.NewError(404, "ResourceNotFound", "resource not found")
	}
	return serrors.ErrorInternalError
}

func owner(tx *db.DB, identity scope.Identity, id string, lock bool) (table.Artifact, error) {
	var a table.Artifact
	q := tx.Where("tenant_id = ? AND scope_key = ? AND id = ?", identity.TenantID, identity.Key(), id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	started := time.Now()
	err := q.First(&a).Error
	if lock {
		telemetry.ObserveArtifactLock("alias-write", time.Since(started))
	}
	return a, err
}

func response(tx *db.DB, a table.Artifact, row table.Alias) (*api.AliasResponse, error) {
	out := &api.AliasResponse{ArtifactID: a.ID, Name: row.Name, Revision: row.Revision, DeletedAt: row.DeletedAt}
	if row.DeletedAt != nil {
		t := row.DeletedAt.UTC()
		out.DeletedAt = &t
	}
	if row.VersionID == nil {
		return out, nil
	}
	var v table.Version
	if err := tx.Where("artifact_id = ? AND id = ? AND state = ?", a.ID, *row.VersionID, "ready").
		First(&v).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, notFound()
		}
		return nil, err
	}
	out.Target = &api.VersionIdentity{
		ArtifactID: a.ID,
		ID:         v.ID,
		Version:    v.Number,
		Sequence:   v.Sequence,
		VersionURI: reference.VersionURI(a.TenantID, a.ProjectID, a.Type, a.ID, v.ID),
	}
	return out, nil
}

func (s *Service) Put(c *core.Context, req *api.PutAliasRequest) (*api.AliasResponse, error) {
	if req.ExpectedRevision == nil || req.VersionID == "" {
		return nil, invalid("versionID and expectedRevision are required")
	}
	return s.mutate(c, req.ArtifactID, func(tx *db.DB) (*table.Alias, error) {
		row, err := PreparePut(tx, req.ArtifactID, req.Name, *req.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		return ApplyPut(tx, row, req.VersionID)
	})
}

func (s *Service) Delete(c *core.Context, req *api.DeleteAliasRequest) (*api.AliasResponse, error) {
	if req.ExpectedRevision == nil {
		return nil, invalid("expectedRevision is required")
	}
	return s.mutate(c, req.ArtifactID, func(tx *db.DB) (*table.Alias, error) {
		return Delete(tx, req.ArtifactID, req.Name, *req.ExpectedRevision)
	})
}

func (s *Service) mutate(
	c *core.Context,
	artifactID string,
	fn func(*db.DB) (*table.Alias, error),
) (*api.AliasResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	var out *api.AliasResponse
	callbackCompleted := false
	err = db.Transaction(
		c.Request.Context(),
		s.database,
		db.TransactionOptions{
			Isolation: sql.LevelReadCommitted,
			Retry: &db.TransactionRetry{
				MaxAttempts: 3,
				Timeout:     5 * time.Second,
				Delay:       10 * time.Millisecond,
			},
		},
		func(tx *db.DB) error {
			callbackCompleted = false
			a, err := owner(tx, identity, artifactID, true)
			if err != nil {
				return err
			}
			row, err := fn(tx)
			if err != nil {
				return err
			}
			out, err = response(tx, a, *row)
			callbackCompleted = err == nil
			return err
		},
	)
	if err != nil {
		if callbackCompleted {
			return nil, serrors.NewError(
				503,
				"OperationOutcomeUnknown",
				"operation outcome is unknown; read alias revision with includeDeleted to confirm",
			)
		}
		return nil, databaseError(err)
	}
	return out, nil
}

func (s *Service) Get(c *core.Context, req *api.GetAliasRequest) (*api.AliasResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	name, err := NormalizeName(req.Name)
	if err != nil {
		return nil, err
	}
	var out *api.AliasResponse
	// A shared owner lock keeps the alias and target identity from straddling a move/delete.
	err = db.Transaction(
		c.Request.Context(),
		s.database,
		db.TransactionOptions{
			Isolation: sql.LevelReadCommitted,
			Retry: &db.TransactionRetry{
				MaxAttempts: 3,
				Timeout:     5 * time.Second,
				Delay:       10 * time.Millisecond,
			},
		},
		func(tx *db.DB) error {
			var a table.Artifact
			started := time.Now()
			lockErr := tx.Where("tenant_id = ? AND scope_key = ? AND id = ?", identity.TenantID, identity.Key(), req.ArtifactID).
				Clauses(clause.Locking{Strength: "SHARE"}).
				First(&a).
				Error
			telemetry.ObserveArtifactLock("alias-read", time.Since(started))
			if lockErr != nil {
				return lockErr
			}
			row := table.Alias{ArtifactID: a.ID, Name: name}
			if name == "latest" {
				var v table.Version
				if err := tx.Where("artifact_id = ? AND state = ?", a.ID, "ready").
					Order("sequence DESC").
					First(&v).
					Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return notFound()
					}
					return err
				}
				row.VersionID = &v.ID
			} else {
				if err := tx.Where("artifact_id = ? AND name = ?", a.ID, name).
					First(&row).
					Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return notFound()
					}
					return err
				}
				if row.VersionID == nil && !req.IncludeDeleted {
					return notFound()
				}
			}
			out, err = response(tx, a, row)
			return err
		},
	)
	if err != nil {
		return nil, databaseError(err)
	}
	return out, nil
}
