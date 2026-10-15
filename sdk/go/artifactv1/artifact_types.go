// Package artifactv1 provides the artifact service HTTP client and public contracts.
package artifactv1

type VersionScheme string

const (
	VersionSchemeInteger VersionScheme = "integer"
	VersionSchemeSemver  VersionScheme = "semver"
)

type ArtifactState string

const (
	ArtifactActive   ArtifactState = "active"
	ArtifactArchived ArtifactState = "archived"
)

type ArtifactPath struct {
	ArtifactID string `json:"-" uri:"artifactID" binding:"required" example:"a01"`
}

type PageQuery struct {
	Cursor string `json:"-" form:"cursor"`
	Limit  int    `json:"-" form:"limit" default:"50" binding:"min=1,max=200"`
}
