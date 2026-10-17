package table

import "time"

type StorageFile struct {
	ID                        string  `gorm:"size:36;primaryKey"`
	TenantID                  string  `gorm:"size:64;not null;uniqueIndex:uk_storage_request,priority:1"`
	ScopeKey                  string  `gorm:"size:160;not null;uniqueIndex:uk_storage_request,priority:2"`
	CreatedBy                 string  `gorm:"size:64;not null;uniqueIndex:uk_storage_request,priority:3"`
	RequestKey                string  `gorm:"size:64;not null;uniqueIndex:uk_storage_request,priority:4"`
	ProjectID                 *string `gorm:"size:64"`
	UserID                    *string `gorm:"size:64"`
	RequestHash               string  `gorm:"size:64;not null"`
	BackendID                 string  `gorm:"size:128;not null;uniqueIndex:uk_storage_location,priority:1"`
	Bucket                    string  `gorm:"size:63;not null;uniqueIndex:uk_storage_location,priority:2"`
	ObjectKey                 string  `gorm:"size:128;not null;uniqueIndex:uk_storage_location,priority:3"`
	ExpectedSize              int64   `gorm:"not null"`
	Size                      *int64
	ContentType               string    `gorm:"size:255;not null"`
	DeclaredChecksumAlgorithm *string   `gorm:"size:32"`
	DeclaredChecksumType      *string   `gorm:"size:16"`
	DeclaredChecksumValue     *string   `gorm:"size:256"`
	State                     string    `gorm:"size:16;not null;index:idx_storage_expiry,priority:1"`
	ExpiresAt                 time.Time `gorm:"not null;index:idx_storage_expiry,priority:2"`
	LastGrantExpiresAt        *time.Time
	DeletedAt                 *time.Time
	NextSweepAt               *time.Time `gorm:"index"`
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}
