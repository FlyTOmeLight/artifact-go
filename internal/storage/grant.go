package storage

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/storage/client"
	"github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

// GrantSigner signs only the exact resources accepted by the storage transaction.
type GrantSigner interface {
	CreateGrant(context.Context, client.GrantRequest) (client.GrantCredentials, error)
	GrantIssuerExpiry() time.Time
	GrantConnection() client.GrantConnection
}

type GrantConfig struct {
	UploadTTL, ReadTTL time.Duration
	AllowedActions     []string
}

// GrantIdentityCheck resolves current identity again after signing a write grant.
type GrantIdentityCheck func(*http.Request) (scope.Identity, error)

func grantActions(s api.GrantScope) ([]string, bool, bool, error) {
	actions := append([]string(nil), s.Actions...)
	if s.Preset != "" {
		switch s.Preset {
		case "upload":
			actions = []string{"s3:PutObject", "s3:ListMultipartUploadParts", "s3:AbortMultipartUpload"}
		case "download":
			actions = []string{"s3:GetObject"}
		default:
			return nil, false, false, fileError(400, "InvalidGrantScope", "unknown scope preset")
		}
	}
	if len(actions) == 0 {
		return nil, false, false, fileError(400, "InvalidGrantScope", "actions must not be empty")
	}
	seen := map[string]bool{}
	var write, read bool
	for _, a := range actions {
		if seen[a] {
			return nil, false, false, fileError(400, "InvalidGrantScope", "duplicate action")
		}
		seen[a] = true
		switch a {
		case "s3:PutObject", "s3:ListMultipartUploadParts", "s3:AbortMultipartUpload":
			write = true
		case "s3:GetObject":
			read = true
		default:
			return nil, false, false, fileError(400, "InvalidGrantScope", "unsupported action")
		}
	}
	sort.Strings(actions)
	return actions, write, read, nil
}

func grantState(row table.StorageFile, now time.Time, write, read bool) error {
	if write && row.State != "uploading" {
		return fileError(409, "FileNotUploading", "upload actions require an uploading file")
	}
	if read && row.State != "ready" && row.State != "bound" {
		return fileError(409, "FileNotReadable", "read actions require a ready or bound file")
	}
	if (write || row.State == "ready") && !now.Before(row.ExpiresAt) {
		return fileError(409, "FileExpired", "file preparation has expired")
	}
	return nil
}

func earlier(a, b time.Time) time.Time {
	if !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

func (c *GrantController) create(ctx *core.Context, req api.CreateGrantRequest) (*api.GrantResponse, error) {
	identity, err := scope.From(ctx)
	if err != nil {
		return nil, err
	}
	actions, write, read, err := grantActions(req.Scope)
	if err != nil {
		return nil, err
	}
	if c.config.AllowedActions != nil {
		allowed := map[string]bool{}
		for _, action := range c.config.AllowedActions {
			allowed[action] = true
		}
		for _, action := range actions {
			if !allowed[action] {
				return nil, fileError(
					403,
					"GrantScopeDenied",
					"requested action is disabled by the service",
				)
			}
		}
	}
	if write {
		if err = identity.RequireActive(); err != nil {
			return nil, err
		}
	}
	ttl := c.config.ReadTTL
	if write {
		ttl = c.config.UploadTTL
	}
	// Compare integer seconds first so an untrusted large value cannot overflow Duration.
	if req.TTLSeconds != nil && *req.TTLSeconds <= int64(ttl/time.Second) {
		ttl = time.Duration(*req.TTLSeconds) * time.Second
	}
	ids := append([]string(nil), req.FileIDs...)
	sort.Strings(ids)
	rows := make(map[string]table.StorageFile, len(ids))
	var deadline time.Time
	var bucket string
	completed := false
	err = db.Transaction(ctx.Request.Context(), c.service.database, fileOptions(), func(tx *db.DB) error {
		completed = false
		// All locks precede the database clock read, including waits behind concurrent cleanup.
		for _, id := range ids {
			row, e := readFile(tx, identity, id, true)
			if e != nil {
				return e
			}
			rows[id] = row
		}
		now, e := table.DatabaseNow(tx)
		if e != nil {
			return e
		}
		deadline = earlier(now.Add(ttl), c.signer.GrantIssuerExpiry())
		bucket = ""
		for _, id := range ids {
			row := rows[id]
			if row.BackendID != c.service.adapter.BackendID() {
				return fileError(503, "StorageBackendUnavailable", "file backend is not configured")
			}
			if bucket != "" && row.Bucket != bucket {
				return fileError(409, "GrantLocationMismatch", "files must use the same tenant bucket")
			}
			bucket = row.Bucket
			if e = grantState(row, now, write, read); e != nil {
				return e
			}
			if write || row.State == "ready" {
				deadline = earlier(deadline, row.ExpiresAt)
			}
		}
		// Millisecond truncation stays below the upper bound and matches both supported databases.
		deadline = deadline.Truncate(time.Millisecond)
		if !deadline.After(now) {
			return fileError(400, "GrantTTLTooShort", "grant deadline has elapsed")
		}
		if write {
			for _, id := range ids {
				row := rows[id]
				if row.LastGrantExpiresAt == nil || row.LastGrantExpiresAt.Before(deadline) {
					if e = tx.Model(&table.StorageFile{}).
						Where("id = ?", id).
						Update("last_grant_expires_at", deadline).
						Error; e != nil {
						return e
					}
				}
			}
		}
		completed = true
		return nil
	})
	if err != nil {
		if completed {
			return nil, fileError(
				503,
				"OperationOutcomeUnknown",
				"grant reservation could not be confirmed; retry authorization",
			)
		}
		return nil, grantDBError(err)
	}
	keys := make([]string, len(req.FileIDs))
	files := make([]api.GrantFile, len(req.FileIDs))
	for n, id := range req.FileIDs {
		row := rows[id]
		keys[n] = row.ObjectKey
		files[n] = api.GrantFile{FileID: id, Bucket: row.Bucket, ObjectKey: row.ObjectKey}
	}
	credentials, err := c.signer.CreateGrant(
		ctx.Request.Context(),
		client.GrantRequest{Bucket: bucket, Keys: keys, Actions: actions, Deadline: deadline},
	)
	if err != nil {
		return nil, err
	}
	if credentials.ExpiresAt.After(deadline) || !time.Now().Before(credentials.ExpiresAt) {
		return nil, fileError(
			503,
			"InvalidGrantExpiration",
			"signer returned credentials outside the authorized deadline",
		)
	}
	if write {
		refreshed, e := c.recheck(ctx.Request)
		if e != nil {
			return nil, e
		}
		if refreshed.UserID != identity.UserID || refreshed.TenantID != identity.TenantID ||
			refreshed.Key() != identity.Key() {
			return nil, fileError(403, "GrantScopeDenied", "authenticated identity changed during signing")
		}
		if e = refreshed.RequireActive(); e != nil {
			return nil, e
		}
		err = db.Transaction(ctx.Request.Context(), c.service.database, fileOptions(), func(tx *db.DB) error {
			for _, id := range ids {
				row, e := readFile(tx, identity, id, true)
				if e != nil {
					return e
				}
				rows[id] = row
			}
			now, e := table.DatabaseNow(tx)
			if e != nil {
				return e
			}
			if !now.Before(credentials.ExpiresAt) {
				return fileError(400, "GrantTTLTooShort", "grant expired before completion")
			}
			for _, id := range ids {
				row := rows[id]
				if e = grantState(row, now, true, false); e != nil {
					return e
				}
				if row.LastGrantExpiresAt == nil ||
					row.LastGrantExpiresAt.Before(credentials.ExpiresAt) {
					return fileError(
						503,
						"InvalidGrantExpiration",
						"grant reservation does not cover issued credentials",
					)
				}
			}
			return nil
		})
		if err != nil {
			return nil, grantDBError(err)
		}
	}
	if !time.Now().Before(credentials.ExpiresAt) {
		return nil, fileError(400, "GrantTTLTooShort", "grant expired before response")
	}
	connection := c.signer.GrantConnection()
	return &api.GrantResponse{
		Endpoint:       connection.Endpoint,
		Region:         connection.Region,
		ForcePathStyle: connection.ForcePathStyle,
		Credentials: api.GrantCredentials{
			AccessKeyID:     credentials.AccessKeyID,
			SecretAccessKey: credentials.SecretAccessKey,
			SessionToken:    credentials.SessionToken,
		},
		ExpiresAt: credentials.ExpiresAt,
		Actions:   actions,
		Files:     files,
	}, nil
}

func grantDBError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fileDBError(err)
}
