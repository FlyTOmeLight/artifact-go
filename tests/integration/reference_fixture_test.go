package integration_test

import (
	"context"
	"net/http"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/app/alias"
	"github.com/FlyTOmeLight/artifact-go/internal/app/artifact"
	"github.com/FlyTOmeLight/artifact-go/internal/app/draft"
	appresolve "github.com/FlyTOmeLight/artifact-go/internal/app/resolve"
	"github.com/FlyTOmeLight/artifact-go/internal/app/retention"
	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/service"
)

// referenceFixture injects deterministic reference failures into the actual
// controllers without exposing a test replacement in the public service API.
type referenceFixture struct {
	database   *db.DB
	references storage.ReferenceService
	limits     *service.VersionConfig
}

func (f *referenceFixture) Start(context.Context) error { return nil }

func (f *referenceFixture) Stop(context.Context) error { return nil }

func (f *referenceFixture) RegisterRoutes(router *core.RouterGroup) error {
	limits := service.VersionConfig{
		MaxFilesPerVersion:  1000,
		MaxPayloadBytes:     256 << 10,
		MaxAnnotationsBytes: 64 << 10,
		MaxRequestBodyBytes: 8 << 20,
	}
	if f.limits != nil {
		limits = *f.limits
	}
	recheck := func(r *http.Request) (scope.Identity, error) {
		i, err := resolve(r)
		return scope.Identity{
			UserID:    i.UserID,
			TenantID:  i.TenantID,
			ProjectID: i.ProjectID,
			Active:    i.OwnershipState == service.OwnershipActive,
		}, err
	}
	group := router.Group("/v1")
	group.Use(func(ctx *core.Context) {
		i, err := recheck(ctx.Request)
		if err != nil {
			serrors.AbortWithError(ctx, err)
			return
		}
		scope.Set(ctx, i)
		ctx.Next()
	})
	artifact.NewController(artifact.NewService(f.database), limits.MaxRequestBodyBytes).RegisterRoutes(group)
	version.NewController(version.NewService(f.database, version.Limits{MaxFilesPerVersion: limits.MaxFilesPerVersion, MaxPayloadBytes: limits.MaxPayloadBytes, MaxAnnotationsBytes: limits.MaxAnnotationsBytes, MaxRequestBodyBytes: limits.MaxRequestBodyBytes}).WithReferences(f.references, recheck)).
		RegisterRoutes(group)
	draft.NewController(draft.NewService(f.database, draft.Limits{MaxFilesPerVersion: limits.MaxFilesPerVersion, MaxPayloadBytes: limits.MaxPayloadBytes, MaxAnnotationsBytes: limits.MaxAnnotationsBytes, MaxRequestBodyBytes: limits.MaxRequestBodyBytes}).WithReferences(f.references, recheck)).
		RegisterRoutes(group)
	alias.NewController(alias.NewService(f.database, limits.MaxRequestBodyBytes)).RegisterRoutes(group)
	retention.NewController(retention.NewService(f.database, limits.MaxRequestBodyBytes)).RegisterRoutes(group)
	appresolve.NewController(appresolve.NewService(f.database, limits.MaxRequestBodyBytes)).RegisterRoutes(group)
	return nil
}
