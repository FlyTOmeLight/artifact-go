package integration_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	storagetable "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestStorageFileUnknownCommitOutcome(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		client, _, backend := storageFileHTTP(t, database, nil)
		ctx := storageIdentity(t, "tenant")
		connection, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		defer func() { database.Statement.ConnPool = original }()
		for _, persist := range []bool{true, false} {
			size := int64(3)
			req := api.RegisterFilesRequest{{RequestKey: fmt.Sprintf("lost-%t", persist), Size: &size}}
			database.Statement.ConnPool = &uncertainCommitPool{
				ConnPool:   connection,
				connection: connection,
				persist:    persist,
			}
			result, err := client.RegisterFiles(ctx, req)
			database.Statement.ConnPool = original
			if err != nil || len(result) != 1 {
				t.Fatalf("register transport %v %v", result, err)
			}
			if persist {
				if result[0].Error != nil {
					t.Fatalf("durable register not confirmed: %+v", result[0])
				}
			} else if result[0].Error == nil || result[0].Error.Code != "OperationOutcomeUnknown" {
				t.Fatalf("unconfirmed register: %+v", result[0])
			}
			replay, err := client.RegisterFiles(ctx, req)
			if err != nil || replay[0].Error != nil {
				t.Fatalf("replay: %+v %v", replay, err)
			}
			if persist && result[0].FileID != replay[0].FileID {
				t.Fatal("replay created another file")
			}
			id := replay[0].FileID
			backend.objectSize.Store(size)
			database.Statement.ConnPool = &uncertainCommitPool{
				ConnPool:   connection,
				connection: connection,
				persist:    persist,
			}
			completed, err := client.CompleteFiles(ctx, api.CompleteFilesRequest{id})
			database.Statement.ConnPool = original
			if err != nil || completed[0].Error == nil ||
				completed[0].Error.Code != "OperationOutcomeUnknown" {
				t.Fatalf("complete unknown: %+v %v", completed, err)
			}
			completed, err = client.CompleteFiles(ctx, api.CompleteFilesRequest{id})
			if err != nil || completed[0].Error != nil || completed[0].State != api.FileReady {
				t.Fatalf("complete retry: %+v %v", completed, err)
			}
			database.Statement.ConnPool = &uncertainCommitPool{
				ConnPool:   connection,
				connection: connection,
				persist:    persist,
			}
			_, err = client.DeleteFile(ctx, id)
			database.Statement.ConnPool = original
			if err == nil {
				t.Fatal("delete unknown outcome must be reported")
			}
			deleted, err := client.DeleteFile(ctx, id)
			if err != nil || deleted.State != api.FileDeleting {
				t.Fatalf("delete retry: %+v %v", deleted, err)
			}
		}
	})
}

func TestStorageConfirmationRechecksAfterStat(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		for _, mutation := range []string{"delete", "expire"} {
			t.Run(mutation, func(t *testing.T) {
				entered := make(chan struct{})
				release := make(chan struct{})
				backend := &storageFileS3{}
				s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "HEAD" && strings.Contains(strings.Trim(r.URL.Path, "/"), "/") {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					backend.ServeHTTP(w, r)
				}))
				defer s3.Close()
				client, _, _ := storageFileServer(
					t,
					database,
					service.StorageOptions{
						Config: &service.StorageConfig{
							BackendID:       "fixture",
							Endpoint:        s3.URL,
							Region:          "us-east-1",
							AccessKeyID:     "fixture",
							SecretAccessKey: "secret",
							UsePathStyle:    true,
						},
						IdentityResolver:      resolve,
						TenantServiceVerifier: func(*http.Request) error { return nil },
						AutoMigrate:           true,
					},
				)
				ctx := storageIdentity(t, "tenant")
				file := storageRegister(t, client, ctx, "race-"+mutation, 0)
				result := make(chan api.CompleteFilesResponse, 1)
				failure := make(chan error, 1)
				go func() {
					out, err := client.CompleteFiles(ctx, api.CompleteFilesRequest{file.FileID})
					result <- out
					failure <- err
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("Stat did not start")
				}
				if mutation == "delete" {
					if _, err := client.DeleteFile(ctx, file.FileID); err != nil {
						close(release)
						t.Fatal(err)
					}
				} else {
					if err := database.Model(&storagetable.StorageFile{}).
						Where("id = ?", file.FileID).
						Update("expires_at", time.Now().Add(-time.Hour)).
						Error; err != nil {
						close(release)
						t.Fatal(err)
					}
				}
				close(release)
				select {
				case out := <-result:
					if err := <-failure; err != nil || len(out) != 1 || out[0].Error == nil ||
						out[0].Error.Status != 409 {
						t.Fatalf("completion escaped second check: %+v %v", out, err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("completion blocked")
				}
				state, err := client.GetFile(ctx, file.FileID)
				if err != nil || state.State == api.FileReady {
					t.Fatalf("unexpected ready: %+v %v", state, err)
				}
			})
		}
	})
}
