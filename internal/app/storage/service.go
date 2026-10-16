package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	client "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	table "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

type Adapter interface {
	BackendID() string
	CheckBucket(context.Context, string) error
	Stat(context.Context, string, string) (client.ObjectInfo, error)
	Delete(context.Context, string, string) error
}

type Limits struct {
	MaxFileBytes, MaxRequestBodyBytes int64
	TemporaryFileTTL                  time.Duration
}

type Service struct {
	database *db.DB
	adapter  Adapter
	limits   Limits
}

func NewService(database *db.DB, adapter Adapter, limits Limits) *Service {
	return &Service{database: database, adapter: adapter, limits: limits}
}

func fileError(status int, code, message string) error {
	return serrors.NewError(status, code, message)
}

func invalidFile(message string) error { return fileError(400, "InvalidFileRequest", message) }

func fileDBError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fileError(404, "FileNotFound", "file not found")
	}
	var app *serrors.Error
	if errors.As(err, &app) {
		return err
	}
	return serrors.ErrorInternalError
}

func fileOptions() db.TransactionOptions {
	return db.TransactionOptions{
		Isolation: sql.LevelReadCommitted,
		Retry:     &db.TransactionRetry{MaxAttempts: 3, Timeout: 5 * time.Second, Delay: 10 * time.Millisecond},
	}
}

func fileQuery(tx *db.DB, i scope.Identity) *db.DB {
	return tx.Where("tenant_id = ? AND scope_key = ?", i.TenantID, i.Key())
}

func readFile(tx *db.DB, i scope.Identity, id string, lock bool) (table.StorageFile, error) {
	var row table.StorageFile
	q := fileQuery(tx, i)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.First(&row, "id = ?", id).Error
	return row, err
}

func response(row table.StorageFile) *api.FileResponse {
	var checksum *api.FileChecksum
	if row.DeclaredChecksumAlgorithm != nil && row.DeclaredChecksumType != nil && row.DeclaredChecksumValue != nil {
		checksum = &api.FileChecksum{
			Algorithm: *row.DeclaredChecksumAlgorithm,
			Type:      *row.DeclaredChecksumType,
			Value:     *row.DeclaredChecksumValue,
		}
	}
	return &api.FileResponse{
		FileID:           row.ID,
		State:            api.FileState(row.State),
		ExpectedSize:     row.ExpectedSize,
		Size:             row.Size,
		ContentType:      row.ContentType,
		DeclaredChecksum: checksum,
		CreatedAt:        row.CreatedAt.UTC(),
		ExpiresAt:        row.ExpiresAt.UTC(),
	}
}

func normalizeFile(req api.RegisterFileItem) (api.RegisterFileItem, string, error) {
	if req.RequestKey == "" || utf8.RuneCountInString(req.RequestKey) > 64 ||
		strings.ContainsAny(req.RequestKey, "\x00\r\n") {
		return req, "", invalidFile("requestKey is required and must not exceed 64 characters")
	}
	if req.Size == nil || *req.Size < 0 {
		return req, "", invalidFile("size is required and must be nonnegative")
	}
	if req.ContentType == "" {
		req.ContentType = "application/octet-stream"
	}
	media, params, err := mime.ParseMediaType(req.ContentType)
	if err != nil {
		return req, "", invalidFile("invalid contentType")
	}
	req.ContentType = mime.FormatMediaType(media, params)
	if len(req.ContentType) > 255 {
		return req, "", invalidFile("contentType exceeds 255 bytes")
	}
	if c := req.DeclaredChecksum; c != nil {
		valid := c.Algorithm != "" && len(c.Algorithm) <= 32 &&
			(c.Type == "FULL_OBJECT" || c.Type == "COMPOSITE") &&
			c.Value != "" &&
			len(c.Value) <= 256
		for _, r := range c.Algorithm {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				valid = false
			}
		}
		for _, r := range c.Value {
			if unicode.IsControl(r) || unicode.IsSpace(r) {
				valid = false
			}
		}
		if !valid {
			return req, "", invalidFile(
				"declaredChecksum requires algorithm, FULL_OBJECT or COMPOSITE type, and a nonempty value",
			)
		}
	}
	raw, _ := json.Marshal(struct {
		Size        int64
		ContentType string
		Checksum    *api.FileChecksum
	}{Size: *req.Size, ContentType: req.ContentType, Checksum: req.DeclaredChecksum})
	hash := sha256.Sum256(raw)
	return req, hex.EncodeToString(hash[:]), nil
}

func (s *Service) RegisterFile(c *core.Context, req api.RegisterFileItem) (*api.FileResponse, error) {
	i, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	req, hash, err := normalizeFile(req)
	if err != nil {
		return nil, err
	}
	lookup := func(ctx context.Context) (table.StorageFile, error) {
		var row table.StorageFile
		err := fileQuery(
			s.database.WithContext(ctx),
			i,
		).Where("created_by = ? AND request_key = ?", i.UserID, req.RequestKey).
			First(&row).
			Error
		return row, err
	}
	match := func(row table.StorageFile) (*api.FileResponse, error) {
		if row.RequestHash != hash {
			return nil, fileError(
				409,
				"IdempotentParameterMismatch",
				"requestKey was used with different parameters",
			)
		}
		return response(row), nil
	}
	row, err := lookup(c.Request.Context())
	if err == nil {
		return match(row)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fileDBError(err)
	}
	if err = i.RequireActive(); err != nil {
		return nil, err
	}
	if *req.Size > s.limits.MaxFileBytes {
		return nil, fileError(413, "FileTooLarge", "file exceeds configured size limit")
	}
	bucket, err := client.TenantBucket(i.TenantID)
	if err != nil {
		return nil, err
	}
	if err = s.adapter.CheckBucket(c.Request.Context(), bucket); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	row = table.StorageFile{
		ID:           id,
		TenantID:     i.TenantID,
		ProjectID:    i.ProjectID,
		ScopeKey:     i.Key(),
		CreatedBy:    i.UserID,
		RequestKey:   req.RequestKey,
		RequestHash:  hash,
		BackendID:    s.adapter.BackendID(),
		Bucket:       bucket,
		ObjectKey:    "files/" + id,
		ExpectedSize: *req.Size,
		ContentType:  req.ContentType,
		State:        "uploading",
	}
	if req.DeclaredChecksum != nil {
		row.DeclaredChecksumAlgorithm = &req.DeclaredChecksum.Algorithm
		row.DeclaredChecksumType = &req.DeclaredChecksum.Type
		row.DeclaredChecksumValue = &req.DeclaredChecksum.Value
	}
	completed := false
	err = db.Transaction(c.Request.Context(), s.database, fileOptions(), func(tx *db.DB) error {
		completed = false
		now, e := table.DatabaseNow(tx)
		if e != nil {
			return e
		}
		row.CreatedAt = now
		row.UpdatedAt = now
		row.ExpiresAt = now.Add(s.limits.TemporaryFileTTL)
		e = tx.Create(&row).Error
		if e == nil {
			e = tx.First(&row, "id = ?", row.ID).Error
		}
		completed = e == nil
		return e
	})
	if err != nil {
		// A competing insert or lost COMMIT acknowledgement is resolved only by its durable receipt.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
		defer cancel()
		existing, e := lookup(ctx)
		if e == nil {
			return match(existing)
		}
		if completed {
			return nil, fileError(
				503,
				"OperationOutcomeUnknown",
				"registration outcome cannot be confirmed; retry the same requestKey",
			)
		}
		return nil, fileDBError(err)
	}
	return response(row), nil
}

func (s *Service) GetFile(c *core.Context, id string) (*api.FileResponse, error) {
	i, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	row, err := readFile(s.database.WithContext(c.Request.Context()), i, id, false)
	if err != nil {
		return nil, fileDBError(err)
	}
	return response(row), nil
}

func (s *Service) DeleteFile(c *core.Context, id string) (*api.FileResponse, int, error) {
	i, err := scope.From(c)
	if err != nil {
		return nil, 0, err
	}
	var row table.StorageFile
	completed := false
	err = db.Transaction(c.Request.Context(), s.database, fileOptions(), func(tx *db.DB) error {
		completed = false
		var e error
		row, e = readFile(tx, i, id, true)
		if e != nil {
			return e
		}
		if row.State == "bound" {
			return fileError(409, "FileReferenced", "referenced files cannot be deleted")
		}
		if row.State == "deleting" || row.State == "deleted" {
			return nil
		}
		e = MarkDeleting(tx, &row)
		completed = e == nil
		return e
	})
	if err != nil {
		if completed {
			return nil, 0, fileError(
				503,
				"OperationOutcomeUnknown",
				"deletion outcome cannot be confirmed; query the file or retry",
			)
		}
		return nil, 0, fileDBError(err)
	}
	status := 202
	if row.State == "deleted" {
		status = 200
	}
	return response(row), status, nil
}
