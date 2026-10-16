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
	Assembly         string
	ForgeTenant      forgetenant.Config
	TenantManagement struct{ Tokens []string }
}

// Build supplies verified Forge identity to one of the existing service assemblies.
// Run owns the tenant HTTP transport, database modules and services in that order.
func Build(ctx context.Context, runtime *app.Runtime, cfg *Config) error {
	if cfg == nil {
		return errors.New("Forge configuration is required")
	}
	mode, err := validateAssembly(cfg)
	if err != nil {
		return err
	}
	var verify service.TenantServiceVerifier
	if mode != "artifact-only" {
		verify, err = forgetenant.NewManagementVerifier(cfg.TenantManagement.Tokens)
		if err != nil {
			return err
		}
	}
	module := &tenantModule{config: cfg.ForgeTenant}
	if err := runtime.Initialize(ctx, module); err != nil {
		return err
	}
	if mode == "storage-only" {
		return service.BuildStorageWithVerifiers(module.client.Resolve, verify)(ctx, runtime, &cfg.Config)
	}
	return service.BuildWithVerifiers(module.client.Resolve, verify)(ctx, runtime, &cfg.Config)
}

func validateAssembly(cfg *Config) (string, error) {
	mode := cfg.Assembly
	if mode == "" {
		mode = "artifact-only"
	}
	switch mode {
	case "artifact-only":
		if cfg.StorageDB != nil || cfg.Storage != nil || len(cfg.TenantManagement.Tokens) > 0 {
			return "", errors.New(
				"artifact-only must not configure storage resources or tenant management tokens",
			)
		}
	case "storage-only", "combined":
		if cfg.StorageDB == nil || cfg.Storage == nil {
			return "", errors.New("storage-only and combined require Storage and StorageDB")
		}
	default:
		return "", errors.New("Assembly must be artifact-only, storage-only or combined")
	}
	var handoff []string
	if cfg.StorageHandoff != nil {
		handoff = append(handoff, cfg.StorageHandoff.Token)
	}
	if cfg.StorageInternal != nil {
		for _, consumer := range cfg.StorageInternal.Consumers {
			handoff = append(handoff, consumer.Tokens...)
		}
	}
	for _, token := range handoff {
		if token != "" && token == cfg.ForgeTenant.ServiceToken {
			return "", errors.New("tenant API and storage reference credentials must be distinct")
		}
	}
	for _, token := range cfg.TenantManagement.Tokens {
		if token == cfg.ForgeTenant.ServiceToken {
			return "", errors.New("tenant management and tenant API credentials must be distinct")
		}
		for _, referenceToken := range handoff {
			if token != "" && token == referenceToken {
				return "", errors.New(
					"tenant management and storage reference credentials must be distinct",
				)
			}
		}
	}
	return mode, nil
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
