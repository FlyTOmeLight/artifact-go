package table

import "time"

type Draft struct {
	ID          string `gorm:"size:36;primaryKey"`
	ArtifactID  string `gorm:"size:36;not null;index"`
	Payload     []byte `gorm:"not null"`
	Annotations []byte `gorm:"not null"`
	Revision    int64  `gorm:"not null"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Artifact    *Artifact `gorm:"foreignKey:ArtifactID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
