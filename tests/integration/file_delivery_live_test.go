package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	backend "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	artifactapi "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	storageapi "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// liveDeliveryArtifactHTTP uses the real artifact host with a controlled trusted
// identity fixture. It does not claim an external tenant identity integration.
func liveDeliveryArtifactHTTP(
	t *testing.T,
	database *db.DB,
	resolver service.IdentityResolver,
	storageDB *db.DB,
	config *service.StorageConfig,
) *artifactapi.Client {
	t.Helper()
	svc, err := service.New(
		t.Context(),
		database,
		service.Options{
			IdentityResolver:      resolver,
			AutoMigrate:           true,
			StorageConsumerID:     "artifact-delivery",
			StorageDB:             storageDB,
			Storage:               config,
			TenantServiceVerifier: func(*http.Request) error { return nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManagerFromConfig(
		&conf.Config{AppName: "delivery-live", AppCode: 100, RunMode: conf.RunModeTest},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	log, err := logger.NewLogger(&logger.Config{Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	h, err := host.NewHTTPHost(manager, log, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	server := httptest.NewServer(h.Handler())
	t.Cleanup(server.Close)
	return versionClient(t, server.URL)
}

// TestFileDeliveryLive waits for the actual write credential deadline. Run with
// -timeout=20m and ARTIFACT_TEST_DELIVERY_LIVE=1; both engines wait concurrently.
func TestFileDeliveryLive(t *testing.T) {
	if os.Getenv("ARTIFACT_TEST_DELIVERY_LIVE") != "1" {
		t.Skip("explicit real-storage delivery gate required (about 16 minutes)")
	}
	for _, key := range []string{"ARTIFACT_TEST_S3_ENDPOINT", "ARTIFACT_TEST_S3_ACCESS_KEY_ID", "ARTIFACT_TEST_S3_SECRET_ACCESS_KEY", "ARTIFACT_TEST_STS_ACCESS_KEY_ID", "ARTIFACT_TEST_STS_SECRET_ACCESS_KEY", "ARTIFACT_TEST_PG_DSN", "ARTIFACT_TEST_MYSQL_PORT"} {
		if os.Getenv(key) == "" {
			t.Fatalf("required live setting %s is absent", key)
		}
	}
	fixture.ForEachDatabase(t, func(t *testing.T, artifactDB *db.DB) {
		t.Parallel()
		driver := db.DriverPostgreSQL
		if artifactDB.Dialector.Name() == "mysql" {
			driver = db.DriverMySQL
		}
		storageDB := fixture.Open(t, driver)
		tenant := fmt.Sprintf("t_%x", bucketRandom(t))
		resolver := func(*http.Request) (service.RequestIdentity, error) {
			return service.RequestIdentity{
				TenantID:       tenant,
				UserID:         "controlled-delivery-fixture",
				OwnershipState: service.OwnershipActive,
			}, nil
		}
		cfg := &service.StorageConfig{
			BackendID:       "juicefs-delivery",
			Endpoint:        os.Getenv("ARTIFACT_TEST_S3_ENDPOINT"),
			Region:          "us-east-1",
			AccessKeyID:     os.Getenv("ARTIFACT_TEST_S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("ARTIFACT_TEST_S3_SECRET_ACCESS_KEY"),
			UsePathStyle:    true,
			GrantIssuer: &service.StorageGrantIssuerConfig{
				Endpoint:        os.Getenv("ARTIFACT_TEST_S3_ENDPOINT"),
				AccessKeyID:     os.Getenv("ARTIFACT_TEST_STS_ACCESS_KEY_ID"),
				SecretAccessKey: os.Getenv("ARTIFACT_TEST_STS_SECRET_ACCESS_KEY"),
			},
		}
		files, _, _ := storageFileServer(
			t,
			storageDB,
			service.StorageOptions{
				Config:                cfg,
				IdentityResolver:      resolver,
				TenantServiceVerifier: func(*http.Request) error { return nil },
				AutoMigrate:           true,
			},
		)
		references := storage.NewBindingService(storageDB, "artifact-delivery")
		artifacts := liveDeliveryArtifactHTTP(t, artifactDB, resolver, storageDB, cfg)
		adapter, err := backend.New(
			backend.Config{
				BackendID:       cfg.BackendID,
				Endpoint:        cfg.Endpoint,
				Region:          cfg.Region,
				AccessKeyID:     cfg.AccessKeyID,
				SecretAccessKey: cfg.SecretAccessKey,
				UsePathStyle:    true,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		operator := grantS3(
			cfg.Endpoint,
			cfg.Region,
			storageapi.GrantCredentials{AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretAccessKey},
		)
		ctx := t.Context()
		bucket, err := files.CreateTenantBucket(ctx, &storageapi.CreateTenantBucketRequest{TenantID: tenant})
		if err != nil {
			t.Fatal(err)
		}
		var locations []storageapi.GrantFile
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, location := range locations {
				if err := adapter.Delete(cleanup, location.Bucket, location.ObjectKey); err != nil {
					t.Error("cleanup isolated delivery object failed")
				}
			}
			if _, err := operator.DeleteBucket(
				cleanup,
				&s3.DeleteBucketInput{Bucket: aws.String(bucket.Bucket)},
			); err != nil {
				t.Error("cleanup isolated delivery bucket failed")
			}
		})
		content := []byte(`{"model":"fixture"}`)
		size := int64(len(content))
		registered, err := files.RegisterFiles(
			ctx,
			storageapi.RegisterFilesRequest{
				{RequestKey: "config", Size: &size, ContentType: "application/json"},
				{RequestKey: "dataset", Size: &size, ContentType: "application/json"},
			},
		)
		if err != nil || len(registered) != 2 {
			t.Fatalf("register batch failed: %v", err)
		}
		ids := []string{}
		for _, entry := range registered {
			if entry.Error != nil || entry.FileResponse == nil {
				t.Fatalf("register item failed: %+v", entry.Error)
			}
			ids = append(ids, entry.FileID)
		}
		ttl := int64(907)
		upload, err := files.CreateGrant(
			ctx,
			&storageapi.CreateGrantRequest{
				FileIDs:    ids,
				Scope:      storageapi.GrantScope{Preset: "upload"},
				TTLSeconds: &ttl,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		locations = upload.Files
		transfer := grantS3(upload.Endpoint, upload.Region, upload.Credentials)
		for _, location := range locations {
			if _, err := transfer.PutObject(
				ctx,
				&s3.PutObjectInput{
					Bucket: aws.String(location.Bucket),
					Key:    aws.String(location.ObjectKey),
					Body:   bytes.NewReader(content),
				},
			); err != nil {
				t.Fatal(err)
			}
		}
		complete, err := files.CompleteFiles(ctx, storageapi.CompleteFilesRequest(ids))
		if err != nil || len(complete) != 2 {
			t.Fatalf("confirm batch: %v", err)
		}
		for _, entry := range complete {
			if entry.Error != nil || entry.FileResponse == nil || entry.State != storageapi.FileReady ||
				entry.Size == nil ||
				*entry.Size != size {
				t.Fatalf("confirmation %+v", entry)
			}
		}
		a := create(t, artifacts, ctx, "folder-delivery", "model")
		ap := artifactapi.ArtifactPath{ArtifactID: a.ID}
		refs := []artifactapi.FileReference{
			{FileID: ids[0], Path: "agent/config.json"},
			{FileID: ids[1], Path: "dataset/train/item.json"},
		}
		draft, err := artifacts.CreateDraft(
			ctx,
			&artifactapi.CreateDraftRequest{ArtifactPath: ap, RequestKey: "draft", Files: refs},
		)
		if err != nil {
			t.Fatal(err)
		}
		version, err := artifacts.CreateVersion(
			ctx,
			&artifactapi.CreateVersionRequest{
				ArtifactPath:          ap,
				RequestKey:            "publish",
				DraftID:               &draft.ID,
				ExpectedDraftRevision: &draft.Revision,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := artifacts.Resolve(ctx, &artifactapi.ResolveRequest{URI: version.Files[0].FileURI})
		if err != nil || resolved.File == nil {
			t.Fatalf("resolve: %v", err)
		}
		download, err := files.CreateGrant(
			ctx,
			&storageapi.CreateGrantRequest{
				FileIDs:    ids,
				Scope:      storageapi.GrantScope{Preset: "download"},
				TTLSeconds: &ttl,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		reader := grantS3(download.Endpoint, download.Region, download.Credentials)
		for _, location := range download.Files {
			object, err := reader.GetObject(
				ctx,
				&s3.GetObjectInput{
					Bucket: aws.String(location.Bucket),
					Key:    aws.String(location.ObjectKey),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(object.Body)
			closeErr := object.Body.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(got, content) {
				t.Fatal("actual downloaded JSON differs")
			}
		}
		shared := create(t, artifacts, ctx, "shared-delivery", "model")
		sharedPath := artifactapi.ArtifactPath{ArtifactID: shared.ID}
		sharedVersion, err := artifacts.CreateVersion(
			ctx,
			&artifactapi.CreateVersionRequest{
				ArtifactPath: sharedPath,
				RequestKey:   "reuse",
				Files:        refs[:1],
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := artifacts.DeleteDraft(
			ctx,
			&artifactapi.DeleteDraftRequest{
				DraftPath:        artifactapi.DraftPath{ArtifactPath: ap, DraftID: draft.ID},
				ExpectedRevision: &draft.Revision,
			},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := artifacts.DeleteVersion(
			ctx,
			&artifactapi.DeleteVersionRequest{
				VersionPath: artifactapi.VersionPath{ArtifactPath: ap, VersionID: version.ID},
			},
		); err != nil {
			t.Fatal(err)
		}
		runReferenceTasks(t, artifactDB, references)
		if _, err := files.CreateGrant(
			ctx,
			&storageapi.CreateGrantRequest{
				FileIDs:    ids[:1],
				Scope:      storageapi.GrantScope{Preset: "download"},
				TTLSeconds: &ttl,
			},
		); err != nil {
			t.Fatalf("non-last reference prevented actual read grant: %v", err)
		}
		if _, err := artifacts.DeleteVersion(
			ctx,
			&artifactapi.DeleteVersionRequest{
				VersionPath: artifactapi.VersionPath{
					ArtifactPath: sharedPath,
					VersionID:    sharedVersion.ID,
				},
			},
		); err != nil {
			t.Fatal(err)
		}
		runReferenceTasks(t, artifactDB, references)
		if _, err := files.CreateGrant(
			ctx,
			&storageapi.CreateGrantRequest{
				FileIDs:    ids[:1],
				Scope:      storageapi.GrantScope{Preset: "download"},
				TTLSeconds: &ttl,
			},
		); err != nil {
			expectError(t, err, 409, "FileNotReadable")
		} else {
			t.Fatal("last Release still allowed new read grant")
		}
		var stored []st.StorageFile
		if err := storageDB.Where("id IN ?", ids).Find(&stored).Error; err != nil || len(stored) != 2 {
			t.Fatal("storage receipts missing")
		}
		var deadline time.Time
		for _, file := range stored {
			if file.State != "deleting" || file.LastGrantExpiresAt == nil {
				t.Fatal("last release did not retain write deadline")
			}
			if file.LastGrantExpiresAt.After(deadline) {
				deadline = *file.LastGrantExpiresAt
			}
			var task st.StorageTask
			if err := storageDB.Where("target_id = ? AND kind = ?", file.ID, "delete").
				First(&task).
				Error; err != nil ||
				!task.NotBefore.Equal(*file.LastGrantExpiresAt) {
				t.Fatal("GC wait differs from real write deadline")
			}
		}
		worker, err := storage.NewWorker(storageDB, adapter, nil)
		if err != nil {
			t.Fatal(err)
		}
		if claim, err := worker.Claim(ctx); err != nil || claim != nil {
			t.Fatal("GC claimed before actual write expiry")
		}
		// Already-issued write credentials can still arrive late. The real GC must
		// remove this overwrite after expiry, without changing any database deadline.
		if _, err := transfer.PutObject(
			ctx,
			&s3.PutObjectInput{
				Bucket: aws.String(locations[0].Bucket),
				Key:    aws.String(locations[0].ObjectKey),
				Body:   bytes.NewReader(content),
			},
		); err != nil {
			t.Fatalf("late authorized write failed before expiry: %v", err)
		}
		t.Logf(
			"public folder draft/publish/resolve/GET and shared references passed; waiting for unmodified write deadline %s",
			deadline.UTC().Format(time.RFC3339),
		)
		wait := time.NewTimer(time.Until(deadline.Add(time.Second)))
		defer wait.Stop()
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-wait.C:
		}
		if _, err := transfer.PutObject(
			ctx,
			&s3.PutObjectInput{
				Bucket: aws.String(locations[0].Bucket),
				Key:    aws.String(locations[0].ObjectKey),
				Body:   bytes.NewReader(content),
			},
		); err != nil {
			var rejected smithy.APIError
			if !errors.As(err, &rejected) ||
				(rejected.ErrorCode() != "ExpiredToken" && rejected.ErrorCode() != "InvalidAccessKeyId" &&
					rejected.ErrorCode() != "InvalidToken" && rejected.ErrorCode() != "AccessDenied") {
				t.Fatalf(
					"expired upload must receive an explicit authentication rejection, got %T: %v",
					err,
					err,
				)
			}
		} else {
			t.Fatal("expired upload credential still writes")
		}
		// An independent privileged request must still reach this exact object, so a
		// transport outage or backend failure cannot masquerade as credential expiry.
		if _, err := operator.HeadObject(
			ctx,
			&s3.HeadObjectInput{
				Bucket: aws.String(locations[0].Bucket),
				Key:    aws.String(locations[0].ObjectKey),
			},
		); err != nil {
			t.Fatalf("expiry control request failed: %v", err)
		}
		for range len(ids) {
			claim, err := worker.Claim(ctx)
			if err != nil || claim == nil {
				t.Fatalf("due GC claim missing: %v", err)
			}
			if err := worker.Execute(ctx, claim); err != nil {
				t.Fatal(err)
			}
		}
		for _, location := range locations {
			if _, err := adapter.Stat(
				ctx,
				location.Bucket,
				location.ObjectKey,
			); !errors.Is(
				err,
				backend.ErrObjectNotFound,
			) {
				t.Fatalf("object remains after real GC: %v", err)
			}
		}
		deadline = time.Now().Add(20 * time.Second)
		for {
			runReferenceTasks(t, artifactDB, references)
			var remaining int64
			if err := artifactDB.Model(&table.Version{}).
				Where("id IN ? AND state <> ?", []string{version.ID, sharedVersion.ID}, "deleted").
				Count(&remaining).
				Error; err != nil {
				t.Fatal(err)
			}
			if remaining == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("artifact did not receive physical deletion receipts")
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				t.Fatal(ctx.Err())
			case <-timer.C:
			}
		}
		t.Log(
			"actual credential expiry, late-object GC and artifact deletion receipts passed; no database deadline was modified",
		)
	})
}
