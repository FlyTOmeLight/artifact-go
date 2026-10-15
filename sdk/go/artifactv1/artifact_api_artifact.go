package artifactv1

import "time"

type CreateArtifactRequest struct {
	Type           string        `json:"type" binding:"required,min=1,max=64" example:"model"`
	Name           string        `json:"name" binding:"required,max=128" example:"demo"`
	ProjectID      *string       `json:"projectID,omitempty" binding:"omitempty,max=64"`
	VersionScheme  VersionScheme `json:"versionScheme" binding:"required,oneof=integer semver" example:"integer"`
	InitialVersion string        `json:"initialVersion,omitempty" binding:"omitempty,max=64"`
}

type ListArtifactsRequest struct {
	PageQuery
	Type  string        `json:"-" form:"type" binding:"omitempty,max=64"`
	State ArtifactState `json:"-" form:"state" binding:"omitempty,oneof=active archived"`
}

type GetArtifactRequest struct{ ArtifactPath }

type ArchiveArtifactRequest struct{ ArtifactPath }

type ArtifactResponse struct {
	ID              string            `json:"id"`
	TenantID        string            `json:"tenantID"`
	ProjectID       *string           `json:"projectID"`
	Type            string            `json:"type"`
	Name            string            `json:"name"`
	ArtifactURI     string            `json:"artifactUri"`
	VersionScheme   VersionScheme     `json:"versionScheme"`
	InitialVersion  string            `json:"initialVersion"`
	State           ArtifactState     `json:"state"`
	CreatedBy       string            `json:"createdBy"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	RetentionPolicy RetentionResponse `json:"retentionPolicy"`
}

type ListArtifactsResponse struct {
	Items      []ArtifactResponse `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}
