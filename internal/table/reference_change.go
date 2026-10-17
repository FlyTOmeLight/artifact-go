package table

import "time"

// ReferenceChange retains a small request decision, never a mutable JSON snapshot.
// Pure JSON operations commit an empty reference plan in their local transaction.
type ReferenceChange struct {
	ArtifactID      string    `gorm:"size:36;primaryKey"`
	ActorID         string    `gorm:"size:64;primaryKey"`
	OperationKind   string    `gorm:"size:32;primaryKey"`
	RequestKey      string    `gorm:"size:64;primaryKey"`
	RequestHash     string    `gorm:"size:64;not null"`
	TenantID        string    `gorm:"size:64;not null"`
	ScopeKey        string    `gorm:"size:160;not null"`
	AttemptID       string    `gorm:"size:36;not null"`
	State           string    `gorm:"size:16;not null;index:idx_reference_change_deadline,priority:1"`
	Deadline        time.Time `gorm:"index:idx_reference_change_deadline,priority:2"`
	Plan            []byte    `gorm:"not null"`
	DraftID         string    `gorm:"size:36;not null"`
	AppliedRevision *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Artifact        *Artifact `gorm:"foreignKey:ArtifactID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}
