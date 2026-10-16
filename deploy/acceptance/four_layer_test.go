package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	gormlogger "gorm.io/gorm/logger"

	at "github.com/FlyTOmeLight/artifact-go/internal/table"
	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	a "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	s "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
)

type fourState struct {
	ArtifactID     string
	KeptVersion    string
	RemovedVersion string
	SharedFile     string
	UniqueFile     string
	SharedKey      string
	UniqueKey      string
	Bucket         string
}

func fourOpen(t *testing.T, key string) *db.DB {
	t.Helper()
	dsn := os.Getenv(key)
	if dsn == "" {
		t.Fatalf("missing disposable setting %s", key)
	}
	database, err := db.NewDB(
		t.Context(),
		&db.Config{Driver: db.DriverPostgreSQL, DSN: dsn, MaxOpenConns: 10, MaxIdleConns: 5},
	)
	if err != nil {
		t.Fatal("cannot open disposable database")
	}
	database.Logger = gormlogger.Default.LogMode(gormlogger.Silent)
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return database
}

func fourHost(t *testing.T, svc host.Service) string {
	t.Helper()
	manager, err := conf.NewManagerFromConfig(
		&conf.Config{AppName: "four-layer", AppCode: 100, RunMode: conf.RunModeTest},
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
	serverHost, err := host.NewHTTPHost(manager, log, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverHost.Close() })
	server := httptest.NewServer(serverHost.Handler())
	t.Cleanup(server.Close)
	return server.URL
}

func fourPayload(shared bool) []byte {
	if shared {
		return bytes.Repeat([]byte("shared four-layer recovery bytes\n"), 32768)
	}
	return bytes.Repeat([]byte("unique doomed recovery bytes\n"), 32768)
}

func TestFourLayerRecovery(t *testing.T) {
	mode := os.Getenv("ARTIFACT_FOUR_LAYER_PHASE")
	if mode == "" {
		t.Skip("requires isolated deploy/acceptance/four_layer.py")
	}
	if mode != "seed" && mode != "restore" {
		t.Fatal("invalid four-layer phase")
	}
	artifactDB, storageDB := fourOpen(
		t,
		"ARTIFACT_FOUR_LAYER_ARTIFACT_DSN",
	), fourOpen(
		t,
		"ARTIFACT_FOUR_LAYER_STORAGE_DSN",
	)
	endpoint := os.Getenv("ARTIFACT_FOUR_LAYER_S3")
	cfg := &service.StorageConfig{
		BackendID:       "four-layer-jfs",
		Endpoint:        endpoint,
		Region:          "us-east-1",
		AccessKeyID:     os.Getenv("MINIO_ROOT_USER"),
		SecretAccessKey: os.Getenv("MINIO_ROOT_PASSWORD"),
		UsePathStyle:    true,
	}
	// An explicit identity fixture tests persistence/recovery, not real Forge login.
	resolver := func(*http.Request) (service.RequestIdentity, error) {
		return service.RequestIdentity{
			UserID:         "four-fixture",
			TenantID:       "t_0123456789abcdef01234567",
			OwnershipState: service.OwnershipActive,
		}, nil
	}
	artifactService, err := service.New(
		t.Context(),
		artifactDB,
		service.Options{
			IdentityResolver:   resolver,
			AutoMigrate:        mode == "seed",
			StorageConsumerID:  "four-artifact",
			StorageDB:          storageDB,
			Storage:            cfg,
			StorageAutoMigrate: mode == "seed",
			StorageFiles: &service.StorageFileConfig{
				MaxFileBytes:        8 << 20,
				TemporaryFileTTL:    30 * time.Second,
				MaxRequestBodyBytes: 1 << 20,
			},
			StorageWorker: &service.StorageWorkerConfig{
				Concurrency:   1,
				ScanInterval:  50 * time.Millisecond,
				LeaseDuration: 2 * time.Second,
				RenewInterval: 500 * time.Millisecond,
				SweepInterval: time.Second,
				MaxFailures:   5,
			},
			TenantServiceVerifier: func(*http.Request) error { return nil },
			Worker: &service.WorkerConfig{
				Concurrency:   1,
				ScanInterval:  50 * time.Millisecond,
				LeaseDuration: 2 * time.Second,
				RenewInterval: 500 * time.Millisecond,
				MaxFailures:   5,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	endpointURL := fourHost(t, artifactService)
	files, err := s.NewClient(s.ClientConfig{Endpoint: endpointURL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	artifacts, err := a.NewClient(a.ClientConfig{Endpoint: endpointURL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifacts.Close() })
	operator := s3.New(
		s3.Options{
			Region:           "us-east-1",
			BaseEndpoint:     aws.String(endpoint),
			UsePathStyle:     true,
			HTTPClient:       &http.Client{Timeout: 10 * time.Second},
			RetryMaxAttempts: 1,
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     cfg.AccessKeyID,
					SecretAccessKey: cfg.SecretAccessKey,
				}, nil
			}),
		},
	)
	ctx := t.Context()
	statePath := os.Getenv("ARTIFACT_FOUR_LAYER_STATE")
	if statePath == "" {
		t.Fatal("missing state path")
	}
	var state fourState
	if mode == "seed" {
		bucket, err := files.CreateTenantBucket(
			ctx,
			&s.CreateTenantBucketRequest{TenantID: "t_0123456789abcdef01234567"},
		)
		if err != nil {
			t.Fatal(err)
		}
		state.Bucket = bucket.Bucket
		sharedBytes, uniqueBytes := fourPayload(true), fourPayload(false)
		sharedSize, uniqueSize := int64(len(sharedBytes)), int64(len(uniqueBytes))
		registered, err := files.RegisterFiles(
			ctx,
			s.RegisterFilesRequest{
				{RequestKey: "shared", Size: &sharedSize, ContentType: "application/octet-stream"},
				{RequestKey: "unique", Size: &uniqueSize, ContentType: "application/octet-stream"},
			},
		)
		if err != nil || len(registered) != 2 {
			t.Fatalf("register: %v", err)
		}
		for index, result := range registered {
			if result.Error != nil || result.FileResponse == nil {
				t.Fatal("file registration failed")
			}
			var row st.StorageFile
			if err = storageDB.First(&row, "id = ?", result.FileID).Error; err != nil {
				t.Fatal(err)
			}
			data := sharedBytes
			if index == 1 {
				data = uniqueBytes
			}
			// Test operator PUT intentionally bypasses grants; no STS/credential-expiry claim.
			if _, err = operator.PutObject(
				ctx,
				&s3.PutObjectInput{
					Bucket: aws.String(row.Bucket),
					Key:    aws.String(row.ObjectKey),
					Body:   bytes.NewReader(data),
				},
			); err != nil {
				t.Fatal(err)
			}
			if index == 0 {
				state.SharedFile, state.SharedKey = row.ID, row.ObjectKey
			} else {
				state.UniqueFile, state.UniqueKey = row.ID, row.ObjectKey
			}
		}
		completed, err := files.CompleteFiles(ctx, s.CompleteFilesRequest{state.SharedFile, state.UniqueFile})
		if err != nil || len(completed) != 2 {
			t.Fatalf("complete: %v", err)
		}
		for _, result := range completed {
			if result.Error != nil || result.FileResponse == nil || result.State != s.FileReady {
				t.Fatal("file completion failed")
			}
		}
		artifact, err := artifacts.CreateArtifact(
			ctx,
			&a.CreateArtifactRequest{
				Type:          "model",
				Name:          "four-layer",
				VersionScheme: a.VersionSchemeInteger,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		state.ArtifactID = artifact.ID
		path := a.ArtifactPath{ArtifactID: artifact.ID}
		kept, err := artifacts.CreateVersion(
			ctx,
			&a.CreateVersionRequest{
				ArtifactPath: path,
				RequestKey:   "kept",
				Payload:      json.RawMessage(`{"role":"preserve"}`),
				Files:        []a.FileReference{{Path: "shared.bin", FileID: state.SharedFile}},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		state.KeptVersion = kept.ID
		doomed, err := artifacts.CreateVersion(
			ctx,
			&a.CreateVersionRequest{
				ArtifactPath: path,
				RequestKey:   "doomed",
				Files: []a.FileReference{
					{Path: "shared.bin", FileID: state.SharedFile},
					{Path: "unique.bin", FileID: state.UniqueFile},
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		state.RemovedVersion = doomed.ID
		if _, err = artifacts.DeleteVersion(
			ctx,
			&a.DeleteVersionRequest{VersionPath: a.VersionPath{ArtifactPath: path, VersionID: doomed.ID}},
		); err != nil {
			t.Fatal(err)
		}
		// Workers have not been started: the public deletion request left a durable obligation.
		fourAssertPending(t, artifactDB, storageDB, state)
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(statePath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Log(
			"seed: public file registration/completion + versions + pending deletion; HTTP hosts close before frozen backup",
		)
		return
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	fourAssertPending(t, artifactDB, storageDB, state)
	verifyBytes := func(key string, shared bool) {
		t.Helper()
		object, err := operator.GetObject(
			ctx,
			&s3.GetObjectInput{Bucket: aws.String(state.Bucket), Key: aws.String(key)},
		)
		if err != nil {
			t.Fatal(err)
		}
		content, readErr := io.ReadAll(object.Body)
		object.Body.Close()
		if readErr != nil || sha256.Sum256(content) != sha256.Sum256(fourPayload(shared)) {
			t.Fatal("restored object bytes differ")
		}
	}
	verifyKept := func() {
		t.Helper()
		kept, err := artifacts.GetVersion(
			ctx,
			&a.GetVersionRequest{
				VersionPath: a.VersionPath{
					ArtifactPath: a.ArtifactPath{ArtifactID: state.ArtifactID},
					VersionID:    state.KeptVersion,
				},
			},
		)
		if err != nil || kept.State != a.VersionReady || len(kept.Files) != 1 ||
			kept.Files[0].FileID != state.SharedFile ||
			string(kept.Payload) != `{"role":"preserve"}` {
			t.Fatalf("restored committed version differs: %v", err)
		}
		verifyBytes(state.SharedKey, true)
	}
	verifyKept()
	verifyBytes(state.UniqueKey, false)
	// Both real services now run their production workers against only restored resources.
	if err = artifactService.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := artifactService.Stop(shutdown); err != nil {
			t.Error(err)
		}
	})
	deadline := time.NewTimer(75 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-deadline.C:
			t.Fatal("restored deletion obligations did not settle")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			var version at.Version
			var unique st.StorageFile
			if err = artifactDB.First(&version, "id = ?", state.RemovedVersion).Error; err != nil {
				t.Fatal(err)
			}
			if err = storageDB.First(&unique, "id = ?", state.UniqueFile).Error; err != nil {
				t.Fatal(err)
			}
			if version.State == "deleted" && unique.State == "deleted" {
				break wait
			}
		}
	}
	var unfinished int64
	if err = artifactDB.Model(&at.BackgroundTask{}).
		Where("kind = ? AND state <> ?", "reference-release", "done").
		Count(&unfinished).
		Error; err != nil ||
		unfinished != 0 {
		t.Fatal("restored release obligation unfinished", err)
	}
	var active []st.StorageReference
	if err = storageDB.Where("state = ?", "active").Find(&active).Error; err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].FileID != state.SharedFile || active[0].ConsumerID != "four-artifact" {
		t.Fatal("restored reference ownership differs")
	}
	var released int64
	if err = storageDB.Model(&st.StorageReference{}).
		Where("state = ?", "released").
		Count(&released).
		Error; err != nil ||
		released != 2 {
		t.Fatal("missing recovered release tombstones", err)
	}
	var completedDeletes int64
	if err = storageDB.Model(&st.StorageTask{}).
		Where("kind = ? AND target_id = ? AND state = ?", "delete", state.UniqueFile, "done").
		Count(&completedDeletes).
		Error; err != nil ||
		completedDeletes != 1 {
		t.Fatal("missing completed storage deletion receipt", err)
	}
	var shared st.StorageFile
	if err = storageDB.First(&shared, "id = ?", state.SharedFile).Error; err != nil || shared.State != "bound" {
		t.Fatal("committed shared file reclaimed", err)
	}
	_, err = operator.HeadObject(
		ctx,
		&s3.HeadObjectInput{Bucket: aws.String(state.Bucket), Key: aws.String(state.UniqueKey)},
	)
	var failure smithy.APIError
	if !errors.As(err, &failure) || (failure.ErrorCode() != "NotFound" && failure.ErrorCode() != "NoSuchKey") {
		t.Fatalf("unique object still exists or storage unavailable: %v", err)
	}
	verifyKept()
	t.Log(
		"restore: original ready version + SHA256 bytes preserved; pending release/delete settled; unique object absent; shared reference retained; no database deadlines edited",
	)
}

func fourAssertPending(t *testing.T, artifactDB, storageDB *db.DB, state fourState) {
	t.Helper()
	var version at.Version
	if err := artifactDB.First(
		&version,
		"id = ?",
		state.RemovedVersion,
	).Error; err != nil ||
		version.State != "deleting" {
		t.Fatal("missing frozen deleting version", err)
	}
	var tasks int64
	if err := artifactDB.Model(&at.BackgroundTask{}).
		Where("kind = ? AND state <> ?", "reference-release", "done").
		Count(&tasks).
		Error; err != nil ||
		tasks == 0 {
		t.Fatal("missing durable release obligation", err)
	}
	var references int64
	if err := storageDB.Model(&st.StorageReference{}).
		Where("state = ?", "active").
		Count(&references).
		Error; err != nil ||
		references != 3 {
		t.Fatal("frozen reference set differs", err)
	}
}
