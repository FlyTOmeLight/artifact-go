package storage

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

// ReferenceService manages file references in the storage database within this process.
// The implementation owns a stable consumer ID; callers pass an authenticated scope.
type ReferenceService interface {
	Metadata(context.Context, Scope, []string) ([]FileMetadata, error)
	Bind(context.Context, Scope, []Reference) ([]BindResult, error)
	Release(context.Context, Scope, []Reference) ([]Status, error)
	Statuses(context.Context, Scope, []Reference) ([]Status, error)
}
