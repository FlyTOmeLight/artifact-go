package resolve

import (
	"database/sql"
	"errors"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"gorm.io/gorm"

	artifactapp "github.com/FlyTOmeLight/artifact-go/internal/app/artifact"
	"github.com/FlyTOmeLight/artifact-go/internal/app/retention"
	versionapp "github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/reference"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type Service struct {
	database            *db.DB
	maxRequestBodyBytes int64
}

func NewService(database *db.DB, maxRequestBodyBytes int64) *Service {
	return &Service{database: database, maxRequestBodyBytes: maxRequestBodyBytes}
}

func notFound() error { return serrors.NewError(404, "ResourceNotFound", "resource not found") }

// Resolve reads all business metadata from one primary-database snapshot.
func (s *Service) Resolve(c *core.Context, req *api.ResolveRequest) (*api.ResolveResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	uri, err := reference.Parse(req.URI)
	if err != nil {
		return nil, serrors.NewError(400, "InvalidURI", err.Error())
	}
	if identity.TenantID != uri.TenantID || (identity.ProjectID == nil) != (uri.ProjectID == nil) ||
		(identity.ProjectID != nil && *identity.ProjectID != *uri.ProjectID) {
		return nil, notFound()
	}
	out := &api.ResolveResponse{}
	err = db.Transaction(
		c.Request.Context(),
		s.database,
		db.TransactionOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true},
		func(tx *db.DB) error {
			var a table.Artifact
			if e := tx.Where("id = ? AND tenant_id = ? AND scope_key = ? AND type = ?", uri.ArtifactID, identity.TenantID, identity.Key(), uri.ArtifactType).
				First(&a).
				Error; e != nil {
				return e
			}
			out.Artifact = artifactapp.Response(a)
			policy, e := retention.Load(tx, a.ID)
			if e != nil {
				return e
			}
			out.Artifact.RetentionPolicy = retention.Response(policy)
			if uri.VersionID == "" && uri.Alias == "" {
				return nil
			}
			q := tx.Where("artifact_id = ? AND state = ?", a.ID, "ready")
			if uri.VersionID != "" {
				q = q.Where("id = ?", uri.VersionID)
			} else if uri.Alias == "latest" {
				q = q.Order("sequence DESC")
			} else {
				var alias table.Alias
				if e = tx.Where("artifact_id = ? AND name = ? AND version_id IS NOT NULL", a.ID, uri.Alias).
					First(&alias).
					Error; e != nil {
					return e
				}
				q = q.Where("id = ?", *alias.VersionID)
			}
			var v table.Version
			if e = q.First(&v).Error; e != nil {
				return e
			}
			value := versionapp.Response(a, v)
			if e = versionapp.LoadFiles(tx, &value); e != nil {
				return e
			}
			out.Version = &value
			if uri.FilePath != "" {
				for _, file := range value.Files {
					if file.Path == uri.FilePath {
						f := file
						out.File = &f
						return nil
					}
				}
				return gorm.ErrRecordNotFound
			}
			return nil
		},
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, notFound()
	}
	if err != nil {
		return nil, serrors.ErrorInternalError
	}
	return out, nil
}
