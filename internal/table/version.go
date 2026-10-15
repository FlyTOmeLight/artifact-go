package table

import (
	"time"
)

type Version struct {
	ID         string `gorm:"size:36;primaryKey;uniqueIndex:uk_version_owner,priority:2"`
	ArtifactID string `gorm:"size:36;not null;uniqueIndex:uk_version_owner,priority:1;uniqueIndex:uk_version_seq,priority:1;uniqueIndex:uk_version_number,priority:1;index:idx_version_state,priority:1"`
	Sequence   int64  `gorm:"not null;uniqueIndex:uk_version_seq,priority:2"`
	Number     string `gorm:"size:64;not null;uniqueIndex:uk_version_number,priority:2"`
	Major      *int64
	Minor      *int64
	Patch      *int64

	// Plain []byte selects binary driver binding and preserves JSON bytes unchanged;
	// MySQL JSON converts large numbers to double.
	Payload []byte `gorm:"not null"`

	Annotations []byte `gorm:"not null"`
	Revision    int64  `gorm:"not null"`
	Protected   bool   `gorm:"not null"`
	State       string `gorm:"size:16;not null;index:idx_version_state,priority:2"`
	CreatedBy   string `gorm:"size:64;not null"`
	PublishedAt time.Time
	DeletedAt   *time.Time
	Artifact    *Artifact `gorm:"foreignKey:ArtifactID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
