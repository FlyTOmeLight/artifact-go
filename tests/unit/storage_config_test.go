package unit_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/service"
)

func TestStorageConfiguration(t *testing.T) {
	valid := service.StorageConfig{
		BackendID:       "primary",
		Endpoint:        "http://127.0.0.1:9000",
		Region:          "us-east-1",
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		UsePathStyle:    true,
	}
	for _, tc := range []struct {
		name      string
		change    func(*service.StorageConfig)
		wantValid bool
	}{
		{name: "valid", wantValid: true},
		{name: "HTTPS", change: func(c *service.StorageConfig) { c.Endpoint = "https://s3.example.test" }, wantValid: true},
		{name: "no-endpoint", change: func(c *service.StorageConfig) { c.Endpoint = "" }},
		{name: "invalid-scheme", change: func(c *service.StorageConfig) { c.Endpoint = "ftp://storage.test" }},
		{name: "URL-credentials", change: func(c *service.StorageConfig) { c.Endpoint = "https://user:secret@storage.test" }},
		{name: "URL-path", change: func(c *service.StorageConfig) { c.Endpoint = "https://storage.test/bucket" }},
		{name: "URL-query", change: func(c *service.StorageConfig) { c.Endpoint = "https://storage.test?credential=secret" }},
		{name: "missing-backend", change: func(c *service.StorageConfig) { c.BackendID = "" }},
		{name: "missing-region", change: func(c *service.StorageConfig) { c.Region = "" }},
		{name: "missing-access-key", change: func(c *service.StorageConfig) { c.AccessKeyID = "" }},
		{name: "missing-secret-key", change: func(c *service.StorageConfig) { c.SecretAccessKey = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			if tc.change != nil {
				tc.change(&cfg)
			}
			err := (service.Config{DB: db.Config{Driver: db.DriverPostgreSQL}, Storage: &cfg}).Validate()
			if (err == nil) != tc.wantValid {
				t.Fatalf("Validate=%v valid=%v", err, tc.wantValid)
			}
			if err != nil &&
				(strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "user:secret")) {
				t.Fatal("configuration error disclosed credentials")
			}
		})
	}
	if err := (service.Config{DB: db.Config{Driver: db.DriverPostgreSQL}}).Validate(); err != nil {
		t.Fatalf("optional storage: %v", err)
	}
}

func TestStorageBuildRequiresVerifierBeforeResources(t *testing.T) {
	resolve := func(*http.Request) (service.RequestIdentity, error) { return service.RequestIdentity{}, nil }
	cfg := &service.Config{Storage: &service.StorageConfig{}}
	// A nil runtime establishes that rejection happens before database initialization.
	for _, build := range []struct {
		name string
		run  func() error
	}{
		{name: "explicit", run: func() error { return service.BuildWithVerifiers(resolve, nil)(context.Background(), nil, cfg) }},
		{name: "user-only", run: func() error { return service.BuildWithIdentity(resolve)(context.Background(), nil, cfg) }},
	} {
		t.Run(build.name, func(t *testing.T) {
			err := build.run()
			if err == nil || !strings.Contains(err.Error(), "TenantServiceVerifier") {
				t.Fatalf("build error=%v", err)
			}
		})
	}
}
