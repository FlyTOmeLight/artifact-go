package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/go-sql-driver/mysql"

	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	storageclient "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
)

// Config contains only the resources used by the implemented artifact service.
// Storage is optional; configuring it enables authenticated bucket provisioning.
type Config struct {
	DB            db.Config
	Version       *VersionConfig
	Worker        *WorkerConfig
	Storage       *StorageConfig
	StorageDB     *db.Config
	StorageFiles  *StorageFileConfig
	StorageWorker *StorageWorkerConfig
	StorageGrants *StorageGrantConfig

	// StorageConsumerID is the stable owner of persisted file references.
	StorageConsumerID string
}

// VersionConfig sets deployment-wide JSON and manifest limits; nil selects the documented defaults.
// A supplied configuration must specify every positive limit.
type VersionConfig struct {
	MaxFilesPerVersion  int
	MaxPayloadBytes     int64
	MaxAnnotationsBytes int64
	MaxRequestBodyBytes int64
}

func versionConfig(value *VersionConfig) (VersionConfig, error) {
	if value == nil {
		return VersionConfig{
			MaxFilesPerVersion:  1000,
			MaxPayloadBytes:     256 << 10,
			MaxAnnotationsBytes: 64 << 10,
			MaxRequestBodyBytes: 8 << 20,
		}, nil
	}
	if value.MaxFilesPerVersion <= 0 || value.MaxPayloadBytes <= 0 || value.MaxAnnotationsBytes <= 0 ||
		value.MaxRequestBodyBytes <= 0 {
		return VersionConfig{}, errors.New("all version limits must be positive")
	}
	return *value, nil
}

func (c Config) Validate() error {
	if c.StorageDB == nil && c.Storage != nil && c.Storage.GrantIssuer != nil {
		return errors.New("storage grant issuer requires StorageDB")
	}

	if c.StorageDB == nil && (c.StorageFiles != nil || c.StorageWorker != nil || c.StorageGrants != nil) {
		return errors.New("storage file and worker configuration require StorageDB")
	}
	if c.StorageDB != nil {
		if err := c.validateStorage(); err != nil {
			return err
		}
	}
	if c.Storage != nil {
		if err := c.Storage.clientConfig().Validate(); err != nil {
			return err
		}
	}
	if err := workerConfig(c.Worker).Validate(); err != nil {
		return err
	}
	if _, err := versionConfig(c.Version); err != nil {
		return err
	}
	if c.DB.Driver != db.DriverPostgreSQL && c.DB.Driver != db.DriverMySQL {
		return fmt.Errorf("artifact requires PostgreSQL or MySQL, got %q", c.DB.Driver)
	}
	if c.DB.Driver == db.DriverMySQL && c.DB.DSNParams != "" {
		parsed, err := mysql.ParseDSN("artifact@tcp(localhost:3306)/artifact?" + c.DB.DSNParams)
		if err != nil || !parsed.ParseTime || parsed.Loc != time.UTC {
			return errors.New("artifact MySQL DSNParams must use parseTime=true and loc=UTC")
		}
	}
	return nil
}

// WorkerConfig controls this service's bounded artifact task executor. Nil selects
// the documented development defaults; supplied values must all be positive.
type WorkerConfig struct {
	Concurrency   int
	ScanInterval  time.Duration
	LeaseDuration time.Duration
	RenewInterval time.Duration
	MaxFailures   int
}

func workerConfig(value *WorkerConfig) backgroundtask.Config {
	if value == nil {
		return backgroundtask.DefaultConfig()
	}
	return backgroundtask.Config{
		Concurrency:   value.Concurrency,
		ScanInterval:  value.ScanInterval,
		LeaseDuration: value.LeaseDuration,
		RenewInterval: value.RenewInterval,
		MaxFailures:   value.MaxFailures,
	}
}

// StorageConfig selects the single S3 backend used for tenant bucket provisioning.
// Credentials belong to the service and are never included in API responses.
type StorageConfig struct {
	BackendID       string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	UsePathStyle    bool
	GrantIssuer     *StorageGrantIssuerConfig
}

func (c StorageConfig) clientConfig() storageclient.Config {
	return storageclient.Config{
		BackendID: c.BackendID, Endpoint: c.Endpoint, Region: c.Region,
		AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey,
		SessionToken: c.SessionToken, UsePathStyle: c.UsePathStyle,
		GrantIssuer: c.GrantIssuer.clientConfig(),
	}
}

func (c Config) validateStorage() error {
	if strings.TrimSpace(c.StorageConsumerID) == "" || len(c.StorageConsumerID) > 64 {
		return errors.New("StorageConsumerID must be a nonempty stable identity of at most 64 bytes")
	}
	if c.Storage == nil || c.StorageDB == nil {
		return errors.New("storage backend and database configuration are required")
	}
	if err := c.Storage.clientConfig().Validate(); err != nil {
		return err
	}
	if _, err := storageGrantConfig(c.StorageGrants); err != nil {
		return err
	}
	if c.StorageGrants != nil && c.Storage.GrantIssuer == nil {
		return errors.New("storage grant settings require a grant issuer")
	}
	if _, err := storageFileConfig(c.StorageFiles); err != nil {
		return err
	}
	if err := storageWorkerConfig(c.StorageWorker).Validate(); err != nil {
		return err
	}
	return (Config{DB: *c.StorageDB}).Validate()
}

// StorageGrantIssuerConfig supplies a policy-constrained ordinary IAM identity.
// It is separate from bucket/object administration credentials. ExpiresAt is
// required when SessionToken is supplied, and zero for long-lived IAM keys.
type StorageGrantIssuerConfig struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	ExpiresAt       time.Time
}

func (c *StorageGrantIssuerConfig) clientConfig() *storageclient.GrantIssuerConfig {
	if c == nil {
		return nil
	}
	return &storageclient.GrantIssuerConfig{
		Endpoint:        c.Endpoint,
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
		SessionToken:    c.SessionToken,
		ExpiresAt:       c.ExpiresAt,
	}
}
