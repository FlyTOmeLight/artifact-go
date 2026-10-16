package integration_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestStorageGrantReadDeletionOrdering(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		c, _, sts := grantHTTP(t, database)
		ctx := storageIdentity(t, "tenant")
		f := storageRegister(t, c, ctx, "read-delete", 0)
		done, err := c.CompleteFiles(ctx, api.CompleteFilesRequest{f.FileID})
		if err != nil || done[0].Error != nil {
			t.Fatalf("complete: %+v %v", done, err)
		}
		sts.onSign = func() {
			if _, err := c.DeleteFile(ctx, f.FileID); err != nil {
				t.Errorf("delete during signing: %v", err)
			}
		}
		req := &api.CreateGrantRequest{FileIDs: []string{f.FileID}, Scope: api.GrantScope{Preset: "download"}}
		grant, err := c.CreateGrant(ctx, req)
		if err != nil || grant == nil {
			t.Fatalf("authorized read lost deletion race: %v", err)
		}
		row := grantFileRow(t, database, f.FileID)
		if row.State != "deleting" || row.LastGrantExpiresAt != nil {
			t.Fatalf("read prevented reclamation: %+v", row)
		}
		calls := sts.calls.Load()
		_, err = c.CreateGrant(ctx, req)
		expectError(t, err, 409, "")
		if sts.calls.Load() != calls {
			t.Fatal("new read signed after deletion")
		}
	})
}

func TestStorageGrantUnknownReservationNeverSigns(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		c, _, sts := grantHTTP(t, database)
		ctx := storageIdentity(t, "tenant")
		f := storageRegister(t, c, ctx, "uncertain-grant", 0)
		connection, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		defer func() { database.Statement.ConnPool = original }()
		for _, persist := range []bool{false, true} {
			pool := &uncertainCommitPool{ConnPool: connection, connection: connection, persist: persist}
			database.Statement.ConnPool = pool
			got, err := c.CreateGrant(
				ctx,
				&api.CreateGrantRequest{
					FileIDs: []string{f.FileID},
					Scope:   api.GrantScope{Preset: "upload"},
				},
			)
			expectError(t, err, 503, "OperationOutcomeUnknown")
			if got != nil || sts.calls.Load() != 0 || pool.commits.Load() != 1 {
				t.Fatal("uncertain reservation signed or was blindly replayed")
			}
			database.Statement.ConnPool = original
			row := grantFileRow(t, database, f.FileID)
			if (row.LastGrantExpiresAt != nil) != persist {
				t.Fatal("unexpected durable reservation outcome")
			}
		}
	})
}

func TestStorageGrantRechecksTrustedIdentity(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		s3 := httptest.NewServer(&storageFileS3{})
		t.Cleanup(s3.Close)
		var inactive atomic.Bool
		sts := &grantSTS{onSign: func() { inactive.Store(true) }}
		issuer := httptest.NewServer(sts)
		t.Cleanup(issuer.Close)
		c, _, _ := storageFileServer(t, database, service.StorageOptions{
			Config: &service.StorageConfig{
				BackendID:       "fixture",
				Endpoint:        s3.URL,
				Region:          "us-east-1",
				AccessKeyID:     "admin",
				SecretAccessKey: "test",
				UsePathStyle:    true,
				GrantIssuer: &service.StorageGrantIssuerConfig{
					Endpoint:        issuer.URL,
					AccessKeyID:     "issuer",
					SecretAccessKey: "test",
				},
			},
			IdentityResolver: func(r *http.Request) (service.RequestIdentity, error) {
				i, e := resolve(r)
				if inactive.Load() {
					i.OwnershipState = service.OwnershipDeleted
				}
				return i, e
			},
			TenantServiceVerifier: func(*http.Request) error { return nil }, AutoMigrate: true,
		})
		ctx := storageIdentity(t, "tenant")
		f := storageRegister(t, c, ctx, "identity-recheck", 0)
		grant, err := c.CreateGrant(
			ctx,
			&api.CreateGrantRequest{FileIDs: []string{f.FileID}, Scope: api.GrantScope{Preset: "upload"}},
		)
		expectError(t, err, 403, "ScopeInactive")
		if grant != nil {
			t.Fatal("returned credentials after identity lost ownership")
		}
		if grantFileRow(t, database, f.FileID).LastGrantExpiresAt == nil {
			t.Fatal("identity failure discarded reservation")
		}
	})
}

func TestStorageGrantBoundIgnoresPreparationExpiry(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		c, _, _ := grantHTTP(t, database)
		ctx := storageIdentity(t, "tenant")
		f := storageRegister(t, c, ctx, "bound-read", 0)
		// T09 owns Bind. A persisted bound fixture isolates the T08 read contract.
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", f.FileID).
			Updates(map[string]any{"state": "bound", "expires_at": time.Now().Add(-time.Hour)}).
			Error; err != nil {
			t.Fatal(err)
		}
		grant, err := c.CreateGrant(
			ctx,
			&api.CreateGrantRequest{FileIDs: []string{f.FileID}, Scope: api.GrantScope{Preset: "download"}},
		)
		if err != nil || grant == nil {
			t.Fatalf("bound read used stale preparation expiry: %v", err)
		}
		if grantFileRow(t, database, f.FileID).LastGrantExpiresAt != nil {
			t.Fatal("read wrote upload reservation")
		}
	})
}
