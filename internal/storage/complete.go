package storage

import (
	"errors"

	"github.com/FlyTOmeLight/server-go/db"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/storage/client"
	"github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

func (s *Service) CompleteFile(c *core.Context, id string) (*api.FileResponse, error) {
	i, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	row, err := readFile(s.database.WithContext(c.Request.Context()), i, id, false)
	if err != nil {
		return nil, fileDBError(err)
	}
	if row.State == "ready" || row.State == "bound" {
		return response(row), nil
	}
	if row.State != "uploading" {
		return nil, fileError(409, "FileNotUploading", "file is not uploading")
	}
	if row.BackendID != s.adapter.BackendID() {
		return nil, fileError(503, "StorageBackendUnavailable", "file backend is not configured")
	}
	info, statErr := s.adapter.Stat(c.Request.Context(), row.Bucket, row.ObjectKey)
	// Recheck after I/O even if Stat failed: another confirmation may already have succeeded.
	completed := false
	err = db.Transaction(c.Request.Context(), s.database, fileOptions(), func(tx *db.DB) error {
		completed = false
		var e error
		row, e = readFile(tx, i, id, true)
		if e != nil {
			return e
		}
		if row.State == "ready" || row.State == "bound" {
			return nil
		}
		if row.State != "uploading" {
			return fileError(409, "FileNotUploading", "file is not uploading")
		}
		now, e := table.DatabaseNow(tx)
		if e != nil {
			return e
		}
		if !now.Before(row.ExpiresAt) {
			return fileError(409, "FileExpired", "file preparation has expired")
		}
		if statErr != nil {
			if errors.Is(statErr, client.ErrObjectNotFound) {
				return fileError(409, "FileObjectMissing", "uploaded object does not exist")
			}
			return statErr
		}
		if info.Size != row.ExpectedSize {
			return fileError(409, "FileSizeMismatch", "uploaded object size differs from declaration")
		}
		if info.Size > s.limits.MaxFileBytes {
			return fileError(413, "FileTooLarge", "file exceeds configured size limit")
		}
		row.State = "ready"
		row.Size = &info.Size
		e = tx.Model(&row).Updates(map[string]any{"state": row.State, "size": info.Size}).Error
		completed = e == nil
		return e
	})
	if err != nil {
		if completed {
			return nil, fileError(
				503,
				"OperationOutcomeUnknown",
				"confirmation outcome cannot be confirmed; query or confirm the same file",
			)
		}
		return nil, fileDBError(err)
	}
	return response(row), nil
}
