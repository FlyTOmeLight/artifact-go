package unit_test

import (
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/service"
)

func TestStorageHandoffConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, token string
		valid                 bool
	}{
		{name: "loopback", endpoint: "http://127.0.0.1:18081", token: "test", valid: true},
		{name: "tls", endpoint: "https://storage.example.test", token: "test", valid: true},
		{name: "remote-cleartext", endpoint: "http://storage.example.test", token: "test", valid: false},
		{name: "missing-token", endpoint: "https://storage.example.test", token: "", valid: false},
		{name: "credential-in-url", endpoint: "https://name:secret@storage.example.test", token: "test", valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := service.Config{
				DB:             db.Config{Driver: db.DriverPostgreSQL},
				StorageHandoff: &service.StorageHandoffConfig{Endpoint: tc.endpoint, Token: tc.token},
			}
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate=%v valid=%t", err, tc.valid)
			}
		})
	}
	for _, address := range []string{"0.0.0.0:18081", "storage.example.test:18081", "127.0.0.1", "[::]:18081"} {
		t.Run(address, func(t *testing.T) {
			cfg := service.Config{
				DB:        db.Config{Driver: db.DriverPostgreSQL},
				StorageDB: &db.Config{Driver: db.DriverPostgreSQL},
				Storage: &service.StorageConfig{
					BackendID:       "test",
					Endpoint:        "http://127.0.0.1:9000",
					Region:          "test",
					AccessKeyID:     "test",
					SecretAccessKey: "test",
				},
				StorageInternal: &service.StorageInternalConfig{ListenAddress: address},
			}
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe or malformed listener accepted")
			}
		})
	}
}
