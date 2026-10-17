package table

import "time"

// StorageReference retains an immutable consumer/file association, including release tombstones.
type StorageReference struct {
	ConsumerID              string `gorm:"size:64;primaryKey"`
	ReferenceID             string `gorm:"size:36;primaryKey"`
	FileID                  string `gorm:"size:36;not null;index:idx_storage_reference_active,priority:1"`
	TenantID                string `gorm:"size:64;not null"`
	ScopeKey                string `gorm:"size:160;not null"`
	State                   string `gorm:"size:16;not null;index:idx_storage_reference_active,priority:2"`
	ReleaseRequiresDeletion bool   `gorm:"not null"`
	CreatedAt               time.Time
	UpdatedAt               time.Time
	File                    *StorageFile `gorm:"foreignKey:FileID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
