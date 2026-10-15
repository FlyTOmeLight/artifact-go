package table

import "time"

type RetentionPolicy struct {
	ArtifactID      string `gorm:"size:36;primaryKey"`
	Enabled         bool   `gorm:"not null"`
	MaxVersions     *int64
	MaxAgeSeconds   *int64
	MinVersions     int64  `gorm:"not null"`
	OverflowAction  string `gorm:"size:16;not null"`
	ProtectAliases  bool   `gorm:"not null"`
	Revision        int64  `gorm:"not null"`
	LastEvaluatedAt *time.Time
	LastResult      []byte `gorm:"not null"`
	UpdatedAt       time.Time
	Artifact        *Artifact `gorm:"foreignKey:ArtifactID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
