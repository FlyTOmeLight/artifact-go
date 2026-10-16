// Package forgeapp assembles the optional Forge-authenticated artifact deployment.
package forgeapp

import (
	"context"
	"errors"

	"github.com/FlyTOmeLight/server-go/app"

	"github.com/FlyTOmeLight/artifact-go/integration/forgetenant"
	"github.com/FlyTOmeLight/artifact-go/service"
)

// Config extends the regular service configuration without coupling its domains to Forge.
type Config struct {
	service.Config   `mapstructure:",squash"`
	ForgeTenant      forgetenant.Config
	TenantManagement struct{ Tokens []string }
}

// Build supplies verified Forge identity to the artifact and file storage service.
// Run owns the tenant HTTP transport, database modules and services in that order.
func Build(ctx context.Context, runtime *app.Runtime, cfg *Config) error {
	if cfg == nil {
		return errors.New("Forge configuration is required")
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	verify, err := forgetenant.NewManagementVerifier(cfg.TenantManagement.Tokens)
	if err != nil {
		return err
	}
	module := &tenantModule{config: cfg.ForgeTenant}
	if err := runtime.Initialize(ctx, module); err != nil {
		return err
	}
	return service.BuildWithVerifiers(module.client.Resolve, verify)(ctx, runtime, &cfg.Config)
}

func validateConfig(cfg *Config) error {
	if cfg.Storage == nil || cfg.StorageDB == nil {
		return errors.New("Forge deployment requires Storage and StorageDB")
	}
	for _, token := range cfg.TenantManagement.Tokens {
		if token != "" && token == cfg.ForgeTenant.ServiceToken {
			return errors.New("tenant management and tenant API credentials must be distinct")
		}
	}
	return cfg.Config.Validate()
}

type tenantModule struct {
	config forgetenant.Config
	client *forgetenant.Client
}

func (m *tenantModule) Initialize(context.Context) (err error) {
	m.client, err = forgetenant.New(m.config)
	return err
}

func (m *tenantModule) Close(context.Context) error {
	if m.client == nil {
		return nil
	}
	return m.client.Close()
}
