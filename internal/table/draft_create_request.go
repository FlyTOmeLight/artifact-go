package table

import "time"

type DraftCreateRequest struct {
	ArtifactID  string `gorm:"size:36;primaryKey"`
	ActorID     string `gorm:"size:64;primaryKey"`
	RequestKey  string `gorm:"size:64;primaryKey"`
	RequestHash string `gorm:"size:64;not null"`
	DraftID     string `gorm:"size:36;not null"`
	CreatedAt   time.Time
	Artifact    *Artifact `gorm:"foreignKey:ArtifactID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
