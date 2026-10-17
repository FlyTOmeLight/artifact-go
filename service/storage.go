package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/host"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	storageclient "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	storagetable "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
)

// StorageOptions supplies dependencies for the file storage module. The database
// is borrowed and contains no artifact tables or cross-domain foreign keys.
type StorageOptions struct {
	Config                *StorageConfig
	IdentityResolver      IdentityResolver
	TenantServiceVerifier TenantServiceVerifier
	AutoMigrate           bool
	Files                 *StorageFileConfig
	Worker                *StorageWorkerConfig
	Grants                *StorageGrantConfig
}

// StorageService owns storage HTTP handlers and its reclamation worker.
type StorageService struct {
	buckets  *storage.Controller
	files    *storage.FileController
	grants   *storage.GrantController
	resolver IdentityResolver
	verifier TenantServiceVerifier
	worker   *storage.Worker
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

var _ host.Service = (*StorageService)(nil)

// NewStorage constructs the storage module with its separate database.
func NewStorage(ctx context.Context, database *db.DB, options StorageOptions) (*StorageService, error) {
	if database == nil {
		return nil, errors.New("storage database is required")
	}
	if options.Config == nil {
		return nil, errors.New("storage backend configuration is required")
	}
	if options.IdentityResolver == nil {
		return nil, errors.New("storage IdentityResolver is required")
	}
	if options.TenantServiceVerifier == nil {
		return nil, errors.New("storage TenantServiceVerifier is required")
	}
	if database.Dialector.Name() != "postgres" && database.Dialector.Name() != "mysql" {
		return nil, errors.New("storage requires PostgreSQL or MySQL")
	}
	if database.Dialector.Name() == "mysql" {
		var decoded time.Time
		if err := database.WithContext(ctx).
			Raw("SELECT CAST('2000-01-01 00:00:00' AS DATETIME)").
			Row().
			Scan(&decoded); err != nil {
			return nil, fmt.Errorf("storage MySQL requires parseTime=true and loc=UTC: %w", err)
		}
		if decoded.Location() != time.UTC {
			return nil, errors.New("storage MySQL requires parseTime=true and loc=UTC")
		}
	}
	limits, err := storageFileConfig(options.Files)
	if err != nil {
		return nil, err
	}
	backend, err := storageclient.New(options.Config.clientConfig())
	if err != nil {
		return nil, err
	}
	grantConfig, err := storageGrantConfig(options.Grants)
	if err != nil {
		return nil, err
	}
	if options.Grants != nil && !backend.GrantEnabled() {
		return nil, errors.New("storage grant settings require a grant issuer")
	}
	fileService := storage.NewService(database, backend, limits)
	var grants *storage.GrantController
	if backend.GrantEnabled() {
		grants = storage.NewGrantController(
			fileService,
			backend,
			grantConfig,
			func(r *http.Request) (scope.Identity, error) {
				identity, err := resolveResourceIdentity(options.IdentityResolver, r)
				if err != nil {
					return scope.Identity{}, err
				}
				return scope.Identity{
					UserID:         identity.UserID,
					TenantID:       identity.TenantID,
					ProjectID:      identity.ProjectID,
					ResourceUserID: identity.ResourceUserID,
					Active:         identity.OwnershipState == OwnershipActive,
				}, nil
			},
		)
	}
	workerCfg := storageWorkerConfig(options.Worker)
	worker, err := storage.NewWorker(database, backend, &workerCfg)
	if err != nil {
		return nil, err
	}
	if options.AutoMigrate {
		if err := storagetable.Migrate(ctx, database); err != nil {
			return nil, err
		}
	}
	return &StorageService{
		buckets:  storage.NewController(backend, limits.MaxRequestBodyBytes),
		files:    storage.NewFileController(fileService),
		grants:   grants,
		resolver: options.IdentityResolver,
		verifier: options.TenantServiceVerifier,
		worker:   worker,
	}, nil
}

func (s *StorageService) RegisterRoutes(router *core.RouterGroup) error {
	management := router.Group("/v1")
	management.Use(observeRequests("storage"), tenantServiceMiddleware(s.verifier))
	s.buckets.RegisterRoutes(management)
	user := router.Group("/v1")
	user.Use(observeRequests("storage"), identityMiddleware(s.resolver))
	s.files.RegisterRoutes(user)
	if s.grants != nil {
		s.grants.RegisterRoutes(user)
	}
	return nil
}

func (s *StorageService) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("storage service already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() { defer close(s.done); _ = s.worker.Run(runCtx) }()
	return nil
}

func (s *StorageService) Stop(ctx context.Context) error {
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
	return ctx.Err()
}
