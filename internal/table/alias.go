package table

import "time"

// Alias retains its revision after deletion; a nil target is a tombstone.
type Alias struct {
	ArtifactID    string  `gorm:"size:36;primaryKey"`
	Name          string  `gorm:"size:64;primaryKey"`
	VersionID     *string `gorm:"size:36;index"`
	Revision      int64   `gorm:"not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time
	TargetVersion *Version  `gorm:"foreignKey:ArtifactID,VersionID;references:ArtifactID,ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	Artifact      *Artifact `gorm:"foreignKey:ArtifactID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
