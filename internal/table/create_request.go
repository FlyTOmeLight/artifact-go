package table

import "time"

type CreateRequest struct {
	ArtifactID  string `gorm:"size:36;primaryKey"`
	ActorID     string `gorm:"size:64;primaryKey"`
	RequestKey  string `gorm:"size:64;primaryKey"`
	RequestHash string `gorm:"size:64;not null"`
	VersionID   string `gorm:"size:36;not null"`
	CreatedAt   time.Time
	Version     *Version `gorm:"foreignKey:ArtifactID,VersionID;references:ArtifactID,ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
