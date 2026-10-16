package forgeapp

import (
	"context"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/service"
)

func TestAssemblyBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Config)
		valid     bool
	}{
		{name: "artifact", configure: func(*Config) {}, valid: true},
		{name: "artifact rejects local storage", configure: func(c *Config) { c.Storage = &service.StorageConfig{} }},
		{name: "unknown mode", configure: func(c *Config) { c.Assembly = "other" }},
		{name: "storage needs resources", configure: func(c *Config) { c.Assembly = "storage-only" }},
		{name: "combined", configure: func(c *Config) {
			c.Assembly = "combined"
			c.Storage = &service.StorageConfig{}
			c.StorageDB = &db.Config{}
		}, valid: true},
		{name: "API token reused for references", configure: func(c *Config) { c.StorageHandoff = &service.StorageHandoffConfig{Token: c.ForgeTenant.ServiceToken} }},
		{name: "API token reused for management", configure: func(c *Config) {
			c.Assembly = "combined"
			c.Storage = &service.StorageConfig{}
			c.StorageDB = &db.Config{}
			c.TenantManagement.Tokens = []string{c.ForgeTenant.ServiceToken}
		}},
		{name: "reference token reused for management", configure: func(c *Config) {
			c.Assembly = "combined"
			c.Storage = &service.StorageConfig{}
			c.StorageDB = &db.Config{}
			c.StorageInternal = &service.StorageInternalConfig{Consumers: []service.StorageConsumerConfig{{Tokens: []string{"reference-token"}}}}
			c.TenantManagement.Tokens = []string{"reference-token"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			cfg.ForgeTenant.ServiceToken = strings.Repeat("a", 32)
			tc.configure(&cfg)
			_, err := validateAssembly(&cfg)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestForgeConfigurationDecoding(t *testing.T) {
	snapshot, err := conf.FromSettings(map[string]any{
		"AppName":  "artifact-test",
		"AppCode":  100,
		"RunMode":  "test",
		"Assembly": "artifact-only",
		"DB":       map[string]any{"Driver": "PostgreSQL", "DSN": "postgres://localhost/test"},
		"ForgeTenant": map[string]any{
			"Endpoint":     "http://127.0.0.1:12345",
			"ServiceToken": strings.Repeat("s", 32),
			"Timeout":      "3s",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := snapshot.Unmarshal(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DB.Driver != db.DriverPostgreSQL || cfg.DB.DSN != "postgres://localhost/test" ||
		cfg.ForgeTenant.Endpoint != "http://127.0.0.1:12345" ||
		cfg.ForgeTenant.Timeout.String() != "3s" {
		t.Fatalf("configuration did not decode expected sections")
	}
	if err := Build(context.Background(), &app.Runtime{}, &Config{}); err == nil {
		t.Fatal("missing tenant authentication configuration accepted")
	}
}
