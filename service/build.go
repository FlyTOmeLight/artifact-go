package service

import (
	"context"
	"errors"

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/FlyTOmeLight/server-go/db"
)

// Build fails closed until the deployment supplies a verified identity integration.
// Embedding applications use BuildWithIdentity with their trusted resolver.
func Build(ctx context.Context, runtime *app.Runtime, cfg *Config) error {
	return BuildWithIdentity(nil)(ctx, runtime, cfg)
}

// BuildWithIdentity creates a standalone assembly function for app.Run.
// Runtime owns the database and closes it after the HTTP service has drained.
func BuildWithIdentity(resolve IdentityResolver) func(context.Context, *app.Runtime, *Config) error {
	return BuildWithVerifiers(resolve, nil)
}

// BuildWithVerifiers supplies independent user and tenant-system authentication.
// A configured Storage backend requires verify; ordinary user auth cannot replace it.
func BuildWithVerifiers(
	resolve IdentityResolver,
	verify TenantServiceVerifier,
) func(context.Context, *app.Runtime, *Config) error {
	return func(ctx context.Context, runtime *app.Runtime, cfg *Config) error {
		if resolve == nil {
			return errors.New(
				"artifact IdentityResolver is required; configure a trusted integration using BuildWithIdentity",
			)
		}
		if cfg == nil {
			return errors.New("artifact configuration is required")
		}
		if cfg.Storage != nil && verify == nil {
			return errors.New("storage TenantServiceVerifier is required")
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		databaseConfig := cfg.DB
		if databaseConfig.Driver == db.DriverMySQL && databaseConfig.DSNParams == "" {
			databaseConfig.DSNParams = "charset=utf8mb4&parseTime=true&loc=UTC&interpolateParams=true"
		}
		database := &db.Module{Config: &databaseConfig}
		if err := runtime.Initialize(ctx, database); err != nil {
			return err
		}
		var storageDatabase *db.DB
		var storageAutoMigrate bool
		if cfg.StorageDB != nil {
			module := &db.Module{Config: storageDatabaseConfig(*cfg.StorageDB)}
			if err := runtime.Initialize(ctx, module); err != nil {
				return err
			}
			storageDatabase = module.DB
			storageAutoMigrate = cfg.StorageDB.AutoMigrate
		}
		svc, err := New(
			ctx,
			database.DB,
			Options{
				IdentityResolver:      resolve,
				AutoMigrate:           cfg.DB.AutoMigrate,
				Version:               cfg.Version,
				Worker:                cfg.Worker,
				Storage:               cfg.Storage,
				StorageDB:             storageDatabase,
				StorageAutoMigrate:    storageAutoMigrate,
				StorageFiles:          cfg.StorageFiles,
				StorageWorker:         cfg.StorageWorker,
				StorageGrants:         cfg.StorageGrants,
				StorageInternal:       cfg.StorageInternal,
				StorageHandoff:        cfg.StorageHandoff,
				TenantServiceVerifier: verify,
			},
		)
		if err != nil {
			return err
		}
		return runtime.AddService(svc)
	}
}

// BuildStorageWithVerifiers assembles a storage-only process with its own database.
func BuildStorageWithVerifiers(
	resolve IdentityResolver,
	verify TenantServiceVerifier,
) func(context.Context, *app.Runtime, *Config) error {
	return func(ctx context.Context, runtime *app.Runtime, cfg *Config) error {
		if resolve == nil || verify == nil {
			return errors.New("storage user and tenant-service verifiers are required")
		}
		if cfg == nil {
			return errors.New("storage configuration is required")
		}
		if err := cfg.validateStorage(); err != nil {
			return err
		}
		module := &db.Module{Config: storageDatabaseConfig(*cfg.StorageDB)}
		if err := runtime.Initialize(ctx, module); err != nil {
			return err
		}
		svc, err := NewStorage(
			ctx,
			module.DB,
			StorageOptions{
				Config:                cfg.Storage,
				IdentityResolver:      resolve,
				TenantServiceVerifier: verify,
				AutoMigrate:           cfg.StorageDB.AutoMigrate,
				Files:                 cfg.StorageFiles,
				Worker:                cfg.StorageWorker,
				Grants:                cfg.StorageGrants,
				Internal:              cfg.StorageInternal,
			},
		)
		if err != nil {
			return err
		}
		return runtime.AddService(svc)
	}
}

func storageDatabaseConfig(c db.Config) *db.Config {
	if c.Driver == db.DriverMySQL && c.DSNParams == "" {
		c.DSNParams = "charset=utf8mb4&parseTime=true&loc=UTC&interpolateParams=true"
	}
	return &c
}
