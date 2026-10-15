package unit_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/service"
)

func TestStorageModuleConfiguration(t *testing.T) {
	backend := &service.StorageConfig{
		BackendID:       "test",
		Endpoint:        "http://127.0.0.1:9000",
		Region:          "us-east-1",
		AccessKeyID:     "test",
		SecretAccessKey: "test",
	}
	valid := func() service.Config {
		return service.Config{
			DB:        db.Config{Driver: db.DriverPostgreSQL},
			StorageDB: &db.Config{Driver: db.DriverPostgreSQL},
			Storage:   backend,
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*service.Config)
		valid  bool
	}{
		{name: "defaults", valid: true},
		{name: "missing-backend", change: func(c *service.Config) { c.Storage = nil }},
		{name: "invalid-storage-driver", change: func(c *service.Config) { c.StorageDB.Driver = "SQLite" }},
		{name: "invalid-file-limits", change: func(c *service.Config) { c.StorageFiles = &service.StorageFileConfig{MaxFileBytes: 1} }},
		{name: "invalid-worker", change: func(c *service.Config) { c.StorageWorker = &service.StorageWorkerConfig{Concurrency: 1} }},
		{name: "file-settings-without-db", change: func(c *service.Config) {
			c.StorageDB = nil
			c.StorageFiles = &service.StorageFileConfig{MaxFileBytes: 1, TemporaryFileTTL: time.Hour, MaxRequestBodyBytes: 1024}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			if tc.change != nil {
				tc.change(&c)
			}
			err := c.Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("Validate=%v valid=%t", err, tc.valid)
			}
		})
	}
	resolver := func(*http.Request) (service.RequestIdentity, error) { return service.RequestIdentity{}, nil }
	for _, verify := range []service.TenantServiceVerifier{nil, func(*http.Request) error { return nil }} {
		if err := service.BuildStorageWithVerifiers(
			resolver,
			verify,
		)(
			context.Background(),
			nil,
			nil,
		); err == nil {
			t.Fatal("accepted missing config or verifier")
		}
	}
}
