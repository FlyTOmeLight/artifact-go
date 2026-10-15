package unit_test

import (
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/service"
)

func TestStorageGrantConfiguration(t *testing.T) {
	makeConfig := func() service.Config {
		return service.Config{
			DB:        db.Config{Driver: db.DriverPostgreSQL},
			StorageDB: &db.Config{Driver: db.DriverPostgreSQL},
			Storage: &service.StorageConfig{
				BackendID:       "test",
				Endpoint:        "http://localhost:9000",
				Region:          "us-east-1",
				AccessKeyID:     "admin",
				SecretAccessKey: "admin-secret",
				GrantIssuer: &service.StorageGrantIssuerConfig{
					Endpoint:        "http://localhost:9000",
					AccessKeyID:     "issuer",
					SecretAccessKey: "issuer-secret",
				},
			},
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*service.Config)
		valid  bool
	}{
		{name: "default", valid: true},
		{name: "root-key", change: func(c *service.Config) { c.Storage.GrantIssuer.AccessKeyID = "admin" }},
		{name: "temporary-without-expiry", change: func(c *service.Config) { c.Storage.GrantIssuer.SessionToken = "session" }},
		{name: "expired", change: func(c *service.Config) { c.Storage.GrantIssuer.ExpiresAt = time.Now().Add(-time.Second) }},
		{name: "invalid-endpoint", change: func(c *service.Config) { c.Storage.GrantIssuer.Endpoint = "http://user:secret@localhost/" }},
		{name: "no-storage-db", change: func(c *service.Config) { c.StorageDB = nil }},
		{name: "zero-ttl", change: func(c *service.Config) { c.StorageGrants = &service.StorageGrantConfig{} }},
		{name: "unsupported-action", change: func(c *service.Config) {
			c.StorageGrants = &service.StorageGrantConfig{UploadTTL: time.Hour, ReadTTL: time.Hour, AllowedActions: []string{"s3:*"}}
		}},
		{name: "duplicate-action", change: func(c *service.Config) {
			c.StorageGrants = &service.StorageGrantConfig{UploadTTL: time.Hour, ReadTTL: time.Hour, AllowedActions: []string{"s3:GetObject", "s3:GetObject"}}
		}},
		{name: "empty-allowed-disables-all", valid: true, change: func(c *service.Config) {
			c.StorageGrants = &service.StorageGrantConfig{UploadTTL: time.Hour, ReadTTL: time.Hour, AllowedActions: []string{}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := makeConfig()
			if tc.change != nil {
				tc.change(&c)
			}
			if err := c.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate=%v valid=%t", err, tc.valid)
			}
		})
	}
}
