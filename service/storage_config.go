package service

import (
	"errors"
	"slices"
	"time"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
)

// StorageFileConfig controls registration limits and the original preparation window.
// Nil selects 1 GiB, 24 hours and 8 MiB; an explicit value requires all fields.
type StorageFileConfig struct {
	MaxFileBytes        int64
	TemporaryFileTTL    time.Duration
	MaxRequestBodyBytes int64
}

func storageFileConfig(c *StorageFileConfig) (storage.Limits, error) {
	if c == nil {
		return storage.Limits{
			MaxFileBytes:        1 << 30,
			TemporaryFileTTL:    24 * time.Hour,
			MaxRequestBodyBytes: 8 << 20,
		}, nil
	}
	if c.MaxFileBytes <= 0 || c.TemporaryFileTTL <= 0 || c.MaxRequestBodyBytes <= 0 {
		return storage.Limits{}, errors.New("all storage file limits must be positive")
	}
	return storage.Limits{
		MaxFileBytes:        c.MaxFileBytes,
		TemporaryFileTTL:    c.TemporaryFileTTL,
		MaxRequestBodyBytes: c.MaxRequestBodyBytes,
	}, nil
}

// StorageWorkerConfig controls independent storage reclamation. Nil uses the
// documented development defaults; supplied values must all be positive.
type StorageWorkerConfig struct {
	Concurrency   int
	ScanInterval  time.Duration
	LeaseDuration time.Duration
	RenewInterval time.Duration
	SweepInterval time.Duration
	MaxFailures   int
}

func storageWorkerConfig(c *StorageWorkerConfig) storage.WorkerConfig {
	if c == nil {
		return storage.WorkerConfig{
			Concurrency:   2,
			ScanInterval:  5 * time.Second,
			LeaseDuration: time.Minute,
			RenewInterval: 15 * time.Second,
			SweepInterval: time.Hour,
			MaxFailures:   20,
		}
	}
	return storage.WorkerConfig{
		Concurrency:   c.Concurrency,
		ScanInterval:  c.ScanInterval,
		LeaseDuration: c.LeaseDuration,
		RenewInterval: c.RenewInterval,
		SweepInterval: c.SweepInterval,
		MaxFailures:   c.MaxFailures,
	}
}

// StorageGrantConfig sets default and maximum grant TTLs. Nil uses one hour.
type StorageGrantConfig struct {
	UploadTTL, ReadTTL time.Duration
	AllowedActions     []string
}

func storageGrantConfig(c *StorageGrantConfig) (storage.GrantConfig, error) {
	if c == nil {
		return storage.GrantConfig{UploadTTL: time.Hour, ReadTTL: time.Hour}, nil
	}
	if c.UploadTTL <= 0 || c.ReadTTL <= 0 {
		return storage.GrantConfig{}, errors.New("storage grant TTLs must be positive")
	}
	seen := map[string]bool{}
	for _, action := range c.AllowedActions {
		if seen[action] {
			return storage.GrantConfig{}, errors.New("duplicate storage allowed action")
		}
		seen[action] = true
		switch action {
		case "s3:PutObject", "s3:GetObject", "s3:ListMultipartUploadParts", "s3:AbortMultipartUpload":
		default:
			return storage.GrantConfig{}, errors.New("unsupported storage allowed action")
		}
	}
	return storage.GrantConfig{
		UploadTTL:      c.UploadTTL,
		ReadTTL:        c.ReadTTL,
		AllowedActions: slices.Clone(c.AllowedActions),
	}, nil
}
