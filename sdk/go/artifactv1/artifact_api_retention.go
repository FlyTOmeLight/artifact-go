package artifactv1

import "time"

// RetentionRules describes the complete collection policy.
type RetentionRules struct {
	Enabled        bool           `json:"enabled"`
	MaxVersions    *int64         `json:"maxVersions"`
	MaxAgeSeconds  *int64         `json:"maxAgeSeconds"`
	MinVersions    int64          `json:"minVersions"`
	OverflowAction OverflowAction `json:"overflowAction"`
	ProtectAliases bool           `json:"protectAliases"`
}

type OverflowAction string

const (
	OverflowReject       OverflowAction = "reject"
	OverflowDeleteOldest OverflowAction = "deleteOldest"
)

type RetentionResponse struct {
	ArtifactID string `json:"artifactID"`
	RetentionRules
	Revision  int64      `json:"revision"`
	UpdatedAt *time.Time `json:"updatedAt"`
}

// UpdateRetentionRequest replaces every policy field. Nil limits encode explicit null.
type UpdateRetentionRequest struct {
	ArtifactPath
	ExpectedRevision *int64         `json:"expectedRevision" binding:"required,gte=0"`
	Enabled          *bool          `json:"enabled" binding:"required"`
	MaxVersions      *int64         `json:"maxVersions" binding:"omitempty,gt=0"`
	MaxAgeSeconds    *int64         `json:"maxAgeSeconds" binding:"omitempty,gt=0"`
	MinVersions      *int64         `json:"minVersions" binding:"required,gte=0"`
	OverflowAction   OverflowAction `json:"overflowAction" binding:"required,oneof=reject deleteOldest"`
	ProtectAliases   *bool          `json:"protectAliases" binding:"required"`
}

type PreviewRetentionRequest struct{ ArtifactPath }

type RetentionDecision struct {
	Version VersionIdentity `json:"version"`
	Reasons []string        `json:"reasons"`
}

type PreviewRetentionResponse struct {
	PolicyRevision  int64               `json:"policyRevision"`
	CalculatedAt    time.Time           `json:"calculatedAt"`
	Candidates      []RetentionDecision `json:"candidates"`
	Retained        []RetentionDecision `json:"retained"`
	Blocked         bool                `json:"blocked"`
	BlockingReasons []string            `json:"blockingReasons"`
}
