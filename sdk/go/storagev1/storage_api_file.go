package storagev1

import "time"

// FileState describes the storage-owned lifecycle of a file.
type FileState string

const (
	FileUploading FileState = "uploading"
	FileReady     FileState = "ready"
	FileBound     FileState = "bound"
	FileDeleting  FileState = "deleting"
	FileDeleted   FileState = "deleted"
)

// FileChecksum is a client declaration, not a server-verified content digest.
type FileChecksum struct {
	Algorithm string `json:"algorithm"`
	Type      string `json:"type"`
	Value     string `json:"value"`
}

// RegisterFileItem reserves one file. Size must be present, including for empty files.
type RegisterFileItem struct {
	RequestKey       string        `json:"requestKey"`
	Size             *int64        `json:"size"`
	ContentType      string        `json:"contentType,omitempty"`
	DeclaredChecksum *FileChecksum `json:"declaredChecksum,omitempty"`
}

// RegisterFilesRequest is a top-level JSON array of 1–1000 independent items.
type RegisterFilesRequest []RegisterFileItem

// CompleteFilesRequest is a top-level JSON array of 1–1000 file IDs.
type CompleteFilesRequest []string

// FileResponse exposes metadata without physical locations or credentials.
type FileResponse struct {
	UserID           *string       `json:"userID"`
	CreatedBy        string        `json:"createdBy"`
	FileID           string        `json:"fileID"`
	State            FileState     `json:"state"`
	ExpectedSize     int64         `json:"expectedSize"`
	Size             *int64        `json:"size"`
	ContentType      string        `json:"contentType"`
	DeclaredChecksum *FileChecksum `json:"declaredChecksum"`
	CreatedAt        time.Time     `json:"createdAt"`
	ExpiresAt        time.Time     `json:"expiresAt"`
}

// FileError describes an individual batch failure; Status is an HTTP status.
type FileError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// RegisterFileResult contains metadata on success, or only the key and error on failure.
type RegisterFileResult struct {
	RequestKey string `json:"requestKey"`
	*FileResponse
	Error *FileError `json:"error"`
}

// CompleteFileResult contains metadata on success, or only the ID and error on failure.
type CompleteFileResult struct {
	FileID string `json:"fileID"`
	*FileResponse
	Error *FileError `json:"error"`
}

// RegisterFilesResponse preserves input order. A nil call error does not imply all items succeeded.
type RegisterFilesResponse []RegisterFileResult

// CompleteFilesResponse preserves input order. Inspect each item's Error independently.
type CompleteFilesResponse []CompleteFileResult
