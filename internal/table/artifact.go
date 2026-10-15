package table

import "time"

type Artifact struct {
	ID              string  `gorm:"size:36;primaryKey"`
	TenantID        string  `gorm:"size:64;not null;uniqueIndex:uk_artifact_name,priority:1"`
	ProjectID       *string `gorm:"size:64;index"`
	ScopeKey        string  `gorm:"size:80;not null;uniqueIndex:uk_artifact_name,priority:2"`
	Type            string  `gorm:"size:64;not null;uniqueIndex:uk_artifact_name,priority:3"`
	Name            string  `gorm:"size:128;not null;uniqueIndex:uk_artifact_name,priority:4"`
	VersionScheme   string  `gorm:"size:16;not null"`
	InitialVersion  string  `gorm:"size:64;not null"`
	LastSequence    int64   `gorm:"not null"`
	LastMajor       int64   `gorm:"not null"`
	LastMinor       int64   `gorm:"not null"`
	LastPatch       int64   `gorm:"not null"`
	State           string  `gorm:"size:16;not null"`
	LastPublishedAt *time.Time
	NextRetentionAt *time.Time `gorm:"index"`
	CreatedBy       string     `gorm:"size:64;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
