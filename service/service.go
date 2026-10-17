package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/host"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/app/alias"
	"github.com/FlyTOmeLight/artifact-go/internal/app/artifact"
	"github.com/FlyTOmeLight/artifact-go/internal/app/draft"
	"github.com/FlyTOmeLight/artifact-go/internal/app/resolve"
	"github.com/FlyTOmeLight/artifact-go/internal/app/retention"
	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	storageclient "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
)

// Options supplies explicit integration dependencies, not a built-in auth fallback.
type Options struct {
	IdentityResolver      IdentityResolver
	AutoMigrate           bool
	Version               *VersionConfig
	Worker                *WorkerConfig
	Storage               *StorageConfig
	StorageDB             *db.DB
	StorageAutoMigrate    bool
	StorageFiles          *StorageFileConfig
	StorageWorker         *StorageWorkerConfig
	StorageGrants         *StorageGrantConfig
	StorageConsumerID     string
	TenantServiceVerifier TenantServiceVerifier
}

// Service owns its controllers and borrows the caller-owned database.
type Service struct {
	controller     *artifact.Controller
	resolver       IdentityResolver
	buckets        *storage.Controller
	storageService *StorageService
	tenantVerifier TenantServiceVerifier
	versions       *version.Controller
	drafts         *draft.Controller
	aliases        *alias.Controller
	retention      *retention.Controller
	resolve        *resolve.Controller
	worker         *backgroundtask.Worker
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
}

var _ host.Service = (*Service)(nil)

// New constructs the artifact service. The caller closes database after HTTP drains.
func New(ctx context.Context, database *db.DB, options Options) (*Service, error) {
	var references storage.ReferenceService
	if options.StorageDB != nil {
		if strings.TrimSpace(options.StorageConsumerID) == "" || len(options.StorageConsumerID) > 64 {
			return nil, errors.New(
				"StorageConsumerID must be a nonempty stable identity of at most 64 bytes",
			)
		}
		references = storage.NewBindingService(options.StorageDB, options.StorageConsumerID)
	}
	if options.StorageDB == nil && options.Storage != nil && options.Storage.GrantIssuer != nil {
		return nil, errors.New("storage grant issuer requires StorageDB")
	}

	if options.StorageDB == nil &&
		(options.StorageFiles != nil || options.StorageWorker != nil || options.StorageGrants != nil || options.StorageAutoMigrate) {
		return nil, errors.New("storage file and worker options require StorageDB")
	}
	var buckets *storage.Controller
	var storageService *StorageService
	if options.StorageDB != nil {
		var err error
		storageService, err = NewStorage(
			ctx,
			options.StorageDB,
			StorageOptions{
				Config:                options.Storage,
				IdentityResolver:      options.IdentityResolver,
				TenantServiceVerifier: options.TenantServiceVerifier,
				AutoMigrate:           options.StorageAutoMigrate,
				Files:                 options.StorageFiles,
				Worker:                options.StorageWorker,
				Grants:                options.StorageGrants,
			},
		)
		if err != nil {
			return nil, err
		}
	}
	if options.Storage != nil {
		if options.TenantServiceVerifier == nil {
			return nil, errors.New("storage TenantServiceVerifier is required")
		}
	}
	limits, err := versionConfig(options.Version)
	if err != nil {
		return nil, err
	}
	if options.Storage != nil && storageService == nil {
		client, err := storageclient.New(options.Storage.clientConfig())
		if err != nil {
			return nil, err
		}
		buckets = storage.NewController(client, limits.MaxRequestBodyBytes)
	}
	if database == nil {
		return nil, errors.New("artifact database is required")
	}
	if options.IdentityResolver == nil {
		return nil, errors.New("artifact IdentityResolver is required")
	}
	if database.Dialector.Name() != "postgres" && database.Dialector.Name() != "mysql" {
		return nil, errors.New("artifact requires PostgreSQL or MySQL")
	}
	if database.Dialector.Name() == "mysql" {
		var decoded time.Time
		if err := database.WithContext(ctx).
			Raw("SELECT CAST('2000-01-01 00:00:00' AS DATETIME)").
			Row().
			Scan(&decoded); err != nil {
			return nil, fmt.Errorf(
				"artifact MySQL connection must decode DATETIME with parseTime=true and loc=UTC: %w",
				err,
			)
		}
		if decoded.Location() != time.UTC {
			return nil, errors.New(
				"artifact MySQL connection must decode DATETIME with parseTime=true and loc=UTC",
			)
		}
	}
	if options.AutoMigrate {
		if err := table.Migrate(ctx, database); err != nil {
			return nil, err
		}
	}
	config := workerConfig(options.Worker)
	worker, err := backgroundtask.New(database, &config)
	if err != nil {
		return nil, err
	}
	worker.WithReferences(references)
	recheck := func(r *http.Request) (scope.Identity, error) {
		i, err := resolveResourceIdentity(options.IdentityResolver, r)
		if err != nil {
			return scope.Identity{}, err
		}
		return scope.Identity{
			UserID:         i.UserID,
			TenantID:       i.TenantID,
			ProjectID:      i.ProjectID,
			ResourceUserID: i.ResourceUserID,
			Active:         i.OwnershipState == OwnershipActive,
		}, nil
	}
	return &Service{
		buckets:        buckets,
		storageService: storageService,
		tenantVerifier: options.TenantServiceVerifier,
		controller:     artifact.NewController(artifact.NewService(database), limits.MaxRequestBodyBytes),
		resolver:       options.IdentityResolver,
		drafts: draft.NewController(
			draft.NewService(
				database,
				draft.Limits{
					MaxFilesPerVersion:  limits.MaxFilesPerVersion,
					MaxPayloadBytes:     limits.MaxPayloadBytes,
					MaxAnnotationsBytes: limits.MaxAnnotationsBytes,
					MaxRequestBodyBytes: limits.MaxRequestBodyBytes,
				},
			).WithReferences(references, recheck),
		),
		worker:    worker,
		resolve:   resolve.NewController(resolve.NewService(database, limits.MaxRequestBodyBytes)),
		retention: retention.NewController(retention.NewService(database, limits.MaxRequestBodyBytes)),
		aliases:   alias.NewController(alias.NewService(database, limits.MaxRequestBodyBytes)),
		versions: version.NewController(
			version.NewService(
				database,
				version.Limits{
					MaxFilesPerVersion:  limits.MaxFilesPerVersion,
					MaxPayloadBytes:     limits.MaxPayloadBytes,
					MaxAnnotationsBytes: limits.MaxAnnotationsBytes,
					MaxRequestBodyBytes: limits.MaxRequestBodyBytes,
				},
			).WithReferences(references, recheck),
		),
	}, nil
}

func (s *Service) RegisterRoutes(router *core.RouterGroup) error {
	if s.storageService != nil {
		if err := s.storageService.RegisterRoutes(router); err != nil {
			return err
		}
	}
	if s.buckets != nil {
		management := router.Group("/v1")
		management.Use(observeRequests("storage"), tenantServiceMiddleware(s.tenantVerifier))
		s.buckets.RegisterRoutes(management)
	}
	group := router.Group("/v1")
	group.Use(observeRequests("artifact"), identityMiddleware(s.resolver))
	s.controller.RegisterRoutes(group)
	s.versions.RegisterRoutes(group)
	s.drafts.RegisterRoutes(group)
	s.aliases.RegisterRoutes(group)
	s.retention.RegisterRoutes(group)
	s.resolve.RegisterRoutes(group)
	return nil
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("artifact service already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	if s.storageService != nil {
		if err := s.storageService.Start(runCtx); err != nil {
			cancel()
			return err
		}
	}
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() { defer close(s.done); _ = s.worker.Run(runCtx) }()
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if s.storageService != nil {
		return errors.Join(ctx.Err(), s.storageService.Stop(ctx))
	}
	return ctx.Err()
}
