package artifactv1

import (
	"encoding/json"
	"time"
)

type VersionBump string

const (
	VersionBumpMajor VersionBump = "major"
	VersionBumpMinor VersionBump = "minor"
	VersionBumpPatch VersionBump = "patch"
)

type VersionState string

const (
	VersionReady    VersionState = "ready"
	VersionDeleting VersionState = "deleting"
	VersionDeleted  VersionState = "deleted"
)

type VersionPath struct {
	ArtifactPath
	VersionID string `json:"-" uri:"versionID" binding:"required"`
}

type AliasUpdate struct {
	Name             string `json:"name" binding:"required,max=64"`
	ExpectedRevision *int64 `json:"expectedRevision" binding:"required,gte=0"`
}

// CreateVersionRequest publishes a JSON version with optional initial protection
// and atomic alias changes. Direct content or a specified draft revision supplies
// the complete file manifest; each published version holds its own references.
type CreateVersionRequest struct {
	ArtifactPath
	RequestKey            string          `json:"requestKey" binding:"required,max=64"`
	Payload               json.RawMessage `json:"payload,omitempty" swaggertype:"object"`
	Annotations           json.RawMessage `json:"annotations,omitempty" swaggertype:"object"`
	Bump                  VersionBump     `json:"bump,omitempty" binding:"omitempty,oneof=major minor patch"`
	Files                 []FileReference `json:"files,omitempty" binding:"omitempty,dive"`
	Protected             bool            `json:"protected"`
	Aliases               []AliasUpdate   `json:"aliases,omitempty" binding:"omitempty,dive"`
	DraftID               *string         `json:"draftID,omitempty"`
	ExpectedDraftRevision *int64          `json:"expectedDraftRevision,omitempty"`
}

type ListVersionsRequest struct {
	ArtifactPath
	PageQuery
	Number string `json:"-" form:"number" binding:"omitempty,max=64"`
}

type GetVersionRequest struct{ VersionPath }

type VersionIdentity struct {
	ID         string `json:"id"`
	ArtifactID string `json:"artifactID"`
	Version    string `json:"version"`
	Sequence   int64  `json:"sequence"`
	VersionURI string `json:"versionUri"`
}

type FileReference struct {
	Path   string `json:"path" binding:"required,max=512"`
	FileID string `json:"fileID" binding:"required"`
}

type ChecksumType string

const (
	ChecksumFullObject ChecksumType = "FULL_OBJECT"
	ChecksumComposite  ChecksumType = "COMPOSITE"
)

type FileChecksum struct {
	Algorithm string       `json:"algorithm"`
	Type      ChecksumType `json:"type"`
	Value     string       `json:"value"`
}

type FileResponse struct {
	FileID           string        `json:"fileID"`
	Path             string        `json:"path"`
	FileURI          string        `json:"fileUri"`
	Size             int64         `json:"size"`
	ContentType      string        `json:"contentType"`
	DeclaredChecksum *FileChecksum `json:"declaredChecksum"`
}

type VersionResponse struct {
	VersionIdentity
	Payload     json.RawMessage `json:"payload" swaggertype:"object"`
	Annotations json.RawMessage `json:"annotations" swaggertype:"object"`
	Protected   bool            `json:"protected"`
	Revision    int64           `json:"revision"`
	State       VersionState    `json:"state"`
	Files       []FileResponse  `json:"files"`
	CreatedBy   string          `json:"createdBy"`
	PublishedAt time.Time       `json:"publishedAt"`
}

type ListVersionsResponse struct {
	Items      []VersionResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// VersionDeletedResponse extends the standard error without losing its metadata.
type VersionDeletedResponse struct {
	ErrorResponse
	Version VersionIdentity `json:"version"`
}

// VersionDeletedError retains the original successful publication identity.
type VersionDeletedError struct {
	Cause   error
	Version VersionIdentity
}

func (e *VersionDeletedError) Error() string { return e.Cause.Error() }

func (e *VersionDeletedError) Unwrap() error { return e.Cause }

// UpdateAnnotationsRequest replaces the complete annotations object.
type UpdateAnnotationsRequest struct {
	VersionPath
	ExpectedRevision *int64          `json:"expectedRevision" binding:"required,gte=0"`
	Annotations      json.RawMessage `json:"annotations" binding:"required" swaggertype:"object"`
}

type UpdateProtectionRequest struct {
	VersionPath
	ExpectedRevision *int64 `json:"expectedRevision" binding:"required,gte=0"`
	Protected        *bool  `json:"protected" binding:"required"`
}

type DeleteVersionRequest struct{ VersionPath }

type DeleteVersionResponse struct {
	VersionIdentity
	State VersionState `json:"state"`
}
