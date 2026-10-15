package artifactv1

import (
	"encoding/json"
	"time"
)

type DraftPath struct {
	ArtifactPath
	DraftID string `json:"-" uri:"draftID" binding:"required"`
}

// CreateDraftRequest creates an independent editable snapshot. A source version
// cannot be combined with payload, annotations or files.
type CreateDraftRequest struct {
	ArtifactPath
	RequestKey      string          `json:"requestKey" binding:"required,max=64"`
	SourceVersionID *string         `json:"sourceVersionID,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty" swaggertype:"object"`
	Annotations     json.RawMessage `json:"annotations,omitempty" swaggertype:"object"`
	Files           []FileReference `json:"files,omitempty"`
}

type GetDraftRequest struct{ DraftPath }

type ListDraftsRequest struct {
	ArtifactPath
	PageQuery
}

// UpdateDraftRequest replaces the complete snapshot. Files must be a non-nil
// slice, including an empty slice when removing all files.
type UpdateDraftRequest struct {
	DraftPath
	RequestKey       string          `json:"requestKey" binding:"required,max=64"`
	ExpectedRevision *int64          `json:"expectedRevision" binding:"required,gt=0"`
	Payload          json.RawMessage `json:"payload" swaggertype:"object"`
	Annotations      json.RawMessage `json:"annotations" swaggertype:"object"`
	Files            []FileReference `json:"files"`
}

type DeleteDraftRequest struct {
	DraftPath
	ExpectedRevision *int64 `json:"-" form:"expectedRevision" binding:"required,gt=0"`
}

type DraftIdentity struct {
	ID         string `json:"id"`
	ArtifactID string `json:"artifactID"`
}

type DraftSummary struct {
	DraftIdentity
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type DraftResponse struct {
	DraftSummary
	Payload     json.RawMessage `json:"payload" swaggertype:"object"`
	Annotations json.RawMessage `json:"annotations" swaggertype:"object"`
	Files       []FileResponse  `json:"files"`
}

type ListDraftsResponse struct {
	Items      []DraftSummary `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// UpdateDraftResponse distinguishes the saved revision from the current snapshot
// returned by a replay after another edit.
type UpdateDraftResponse struct {
	AppliedRevision int64         `json:"appliedRevision"`
	Draft           DraftResponse `json:"draft"`
}

type DraftDeletedResponse struct {
	ErrorResponse
	Draft           DraftIdentity `json:"draft"`
	AppliedRevision *int64        `json:"appliedRevision,omitempty"`
}

// DraftDeletedError retains the successful request identity after hard deletion.
type DraftDeletedError struct {
	Cause           error
	Draft           DraftIdentity
	AppliedRevision *int64
}

func (e *DraftDeletedError) Error() string { return e.Cause.Error() }

func (e *DraftDeletedError) Unwrap() error { return e.Cause }
