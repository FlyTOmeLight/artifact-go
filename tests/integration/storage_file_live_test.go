package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// Live validation uploads with the test operator's S3 credential. It does not
// exercise or imply the T08 grant contract.
func TestStorageFileLive(t *testing.T) {
	if os.Getenv("ARTIFACT_TEST_S3_ACCESS_KEY_ID") == "" {
		t.Skip("live credentials required")
	}
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		cfg := &service.StorageConfig{
			BackendID:       "juicefs-local",
			Endpoint:        os.Getenv("ARTIFACT_TEST_S3_ENDPOINT"),
			Region:          "us-east-1",
			AccessKeyID:     os.Getenv("ARTIFACT_TEST_S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("ARTIFACT_TEST_S3_SECRET_ACCESS_KEY"),
			UsePathStyle:    true,
		}
		operator := s3.New(
			s3.Options{
				HTTPClient:       &http.Client{Timeout: 5 * time.Second},
				RetryMaxAttempts: 1,
				Region:           cfg.Region,
				BaseEndpoint:     aws.String(cfg.Endpoint),
				UsePathStyle:     true,
				Credentials: aws.CredentialsProviderFunc(
					func(context.Context) (aws.Credentials, error) {
						return aws.Credentials{
							AccessKeyID:     cfg.AccessKeyID,
							SecretAccessKey: cfg.SecretAccessKey,
						}, nil
					},
				),
			},
		)
		tenant := fmt.Sprintf("t_%x", bucketRandom(t))
		c, _, svc := storageFileServer(
			t,
			database,
			service.StorageOptions{
				Config: cfg,
				IdentityResolver: func(*http.Request) (service.RequestIdentity, error) {
					return service.RequestIdentity{
						TenantID:       tenant,
						UserID:         "probe",
						OwnershipState: service.OwnershipActive,
					}, nil
				},
				TenantServiceVerifier: func(*http.Request) error { return nil },
				AutoMigrate:           true,
				Worker: &service.StorageWorkerConfig{
					Concurrency:   1,
					ScanInterval:  20 * time.Millisecond,
					LeaseDuration: time.Second,
					RenewInterval: 200 * time.Millisecond,
					SweepInterval: 100 * time.Millisecond,
					MaxFailures:   3,
				},
			},
		)
		ctx := t.Context()
		bucket, err := c.CreateTenantBucket(ctx, &api.CreateTenantBucketRequest{TenantID: tenant})
		if err != nil {
			t.Fatal(err)
		}
		// The probe owns this unique empty bucket; cleanup also handles a failed assertion.
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = svc.Stop(cleanup)
			uploads, e := operator.ListMultipartUploads(
				cleanup,
				&s3.ListMultipartUploadsInput{Bucket: aws.String(bucket.Bucket)},
			)
			if e == nil {
				for _, u := range uploads.Uploads {
					_, _ = operator.AbortMultipartUpload(
						cleanup,
						&s3.AbortMultipartUploadInput{
							Bucket:   aws.String(bucket.Bucket),
							Key:      u.Key,
							UploadId: u.UploadId,
						},
					)
				}
			}
			objects, e := operator.ListObjectsV2(
				cleanup,
				&s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket)},
			)
			if e == nil {
				for _, o := range objects.Contents {
					_, _ = operator.DeleteObject(
						cleanup,
						&s3.DeleteObjectInput{Bucket: aws.String(bucket.Bucket), Key: o.Key},
					)
				}
			}
			if _, e = operator.DeleteBucket(
				cleanup,
				&s3.DeleteBucketInput{Bucket: aws.String(bucket.Bucket)},
			); e != nil {
				t.Error("live test bucket cleanup failed")
			}
		})
		f := storageRegister(t, c, ctx, "live-file", 5)
		key := "files/" + f.FileID
		put := func() {
			t.Helper()
			_, e := operator.PutObject(
				ctx,
				&s3.PutObjectInput{
					Bucket: aws.String(bucket.Bucket),
					Key:    aws.String(key),
					Body:   bytes.NewReader([]byte("hello")),
				},
			)
			if e != nil {
				t.Fatal("live object upload failed")
			}
		}
		put()
		completed, err := c.CompleteFiles(ctx, api.CompleteFilesRequest{f.FileID})
		if err != nil || len(completed) != 1 || completed[0].Error != nil ||
			completed[0].State != api.FileReady {
			t.Fatalf("live confirmation: %+v %v", completed, err)
		}
		// Distinct uploads on the exact key must be aborted, while a prefix neighbour survives.
		target, err := operator.CreateMultipartUpload(
			ctx,
			&s3.CreateMultipartUploadInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key)},
		)
		if err != nil {
			t.Fatal("create target multipart failed")
		}
		neighbor, err := operator.CreateMultipartUpload(
			ctx,
			&s3.CreateMultipartUploadInput{
				Bucket: aws.String(bucket.Bucket),
				Key:    aws.String(key + "-neighbor"),
			},
		)
		if err != nil {
			t.Fatal("create neighbour multipart failed")
		}
		if _, err = c.DeleteFile(ctx, f.FileID); err != nil {
			t.Fatal(err)
		}
		if err = svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		awaitStorageLive(
			t,
			func() bool { got, e := c.GetFile(ctx, f.FileID); return e == nil && got.State == api.FileDeleted },
		)
		absent := func() bool {
			_, e := operator.HeadObject(
				ctx,
				&s3.HeadObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key)},
			)
			var detail smithy.APIError
			return errors.As(e, &detail) &&
				(detail.ErrorCode() == "NotFound" || detail.ErrorCode() == "NoSuchKey")
		}
		if !absent() {
			t.Fatal("deleted live object remains")
		}
		uploads, err := operator.ListMultipartUploads(
			ctx,
			&s3.ListMultipartUploadsInput{Bucket: aws.String(bucket.Bucket)},
		)
		if err != nil {
			t.Fatal("list multipart failed")
		}
		neighbourPresent := false
		for _, upload := range uploads.Uploads {
			if aws.ToString(upload.UploadId) == aws.ToString(target.UploadId) {
				t.Error("target multipart remains")
			}
			if aws.ToString(upload.UploadId) == aws.ToString(neighbor.UploadId) {
				neighbourPresent = true
			}
		}
		if !neighbourPresent {
			t.Fatal("prefix neighbour multipart was incorrectly removed")
		}
		put()
		awaitStorageLive(t, absent)
		got, err := c.GetFile(ctx, f.FileID)
		if err != nil || got.State != api.FileDeleted {
			t.Fatalf("late write resurrected metadata: %+v %v", got, err)
		}
	})
}

func awaitStorageLive(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("live storage condition timed out")
		case <-ticker.C:
		}
	}
}
