// Package storagehandoff defines the authenticated HTTP boundary between artifact and storage.
package storagehandoff

import (
	"context"
	"time"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

type Scope struct {
	TenantID  string  `json:"tenantID"`
	ProjectID *string `json:"projectID"`
}

type Reference struct {
	ReferenceID string `json:"referenceID"`
	FileID      string `json:"fileID"`
}

type ConfirmationMetadata struct {
	Size              *int64            `json:"size"`
	ContentType       string            `json:"contentType"`
	DeclaredChecksum  *api.FileChecksum `json:"declaredChecksum"`
	OriginalExpiresAt time.Time         `json:"originalExpiresAt"`
}

type FileMetadata struct {
	FileID string `json:"fileID"`
	State  string `json:"state"`
	ConfirmationMetadata
}

type BindResult struct {
	ReferenceID string               `json:"referenceID"`
	FileID      string               `json:"fileID"`
	State       string               `json:"state"`
	File        ConfirmationMetadata `json:"file"`
}

type Status struct {
	ReferenceID             string `json:"referenceID"`
	FileID                  string `json:"fileID"`
	State                   string `json:"state"`
	ReleaseComplete         bool   `json:"releaseComplete"`
	ReleaseRequiresDeletion *bool  `json:"releaseRequiresDeletion"`
}

type MetadataRequest struct {
	Scope   Scope    `json:"scope"`
	FileIDs []string `json:"fileIDs"`
}

type ReferencesRequest struct {
	Scope      Scope       `json:"scope"`
	References []Reference `json:"references"`
}

type BindingService interface {
	Metadata(context.Context, string, Scope, []string) ([]FileMetadata, error)
	Bind(context.Context, string, Scope, []Reference) ([]BindResult, error)
	Release(context.Context, string, Scope, []Reference) ([]Status, error)
	Statuses(context.Context, string, Scope, []Reference) ([]Status, error)
}

// Error retains a stable remote code and whether an operation may have committed.
type Error struct {
	Status         int
	Code           string
	OutcomeUnknown bool
	Cause          error
}

func (e *Error) Error() string { return e.Code }

func (e *Error) Unwrap() error { return e.Cause }
