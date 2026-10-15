package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestStorageGrantLive(t *testing.T) {
	if os.Getenv("ARTIFACT_TEST_STS_ACCESS_KEY_ID") == "" {
		t.Skip("live ordinary IAM issuer required")
	}
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		cfg := &service.StorageConfig{
			BackendID:       "juicefs-local",
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
		operator := grantS3(
			cfg.Endpoint,
			cfg.Region,
			api.GrantCredentials{AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretAccessKey},
		)
		tenant := fmt.Sprintf("t_%x", bucketRandom(t))
		c, _, _ := storageFileServer(
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
			},
		)
		ctx := t.Context()
		bucket, err := c.CreateTenantBucket(ctx, &api.CreateTenantBucketRequest{TenantID: tenant})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			uploads, e := operator.ListMultipartUploads(
				ctx,
				&s3.ListMultipartUploadsInput{Bucket: aws.String(bucket.Bucket)},
			)
			if e == nil {
				for _, u := range uploads.Uploads {
					_, _ = operator.AbortMultipartUpload(
						ctx,
						&s3.AbortMultipartUploadInput{
							Bucket:   aws.String(bucket.Bucket),
							Key:      u.Key,
							UploadId: u.UploadId,
						},
					)
				}
			}
			objects, e := operator.ListObjectsV2(
				ctx,
				&s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket)},
			)
			if e == nil {
				for _, o := range objects.Contents {
					_, _ = operator.DeleteObject(
						ctx,
						&s3.DeleteObjectInput{Bucket: aws.String(bucket.Bucket), Key: o.Key},
					)
				}
			}
			if _, e = operator.DeleteBucket(
				ctx,
				&s3.DeleteBucketInput{Bucket: aws.String(bucket.Bucket)},
			); e != nil {
				t.Error("cleanup test bucket failed")
			}
		})
		one := storageRegister(t, c, ctx, "one", 5)
		two := storageRegister(t, c, ctx, "multipart", 5)
		req := &api.CreateGrantRequest{
			FileIDs: []string{one.FileID, two.FileID},
			Scope:   api.GrantScope{Preset: "upload"},
		}
		upload, err := c.CreateGrant(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		transfer := grantS3(upload.Endpoint, upload.Region, upload.Credentials)
		first, second := upload.Files[0], upload.Files[1]
		if _, err = transfer.PutObject(
			ctx,
			&s3.PutObjectInput{
				Bucket: aws.String(first.Bucket),
				Key:    aws.String(first.ObjectKey),
				Body:   bytes.NewReader([]byte("hello")),
			},
		); err != nil {
			t.Fatal(err)
		}
		multipart, err := transfer.CreateMultipartUpload(
			ctx,
			&s3.CreateMultipartUploadInput{
				Bucket: aws.String(second.Bucket),
				Key:    aws.String(second.ObjectKey),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		part, err := transfer.UploadPart(
			ctx,
			&s3.UploadPartInput{
				Bucket:     aws.String(second.Bucket),
				Key:        aws.String(second.ObjectKey),
				UploadId:   multipart.UploadId,
				PartNumber: aws.Int32(1),
				Body:       bytes.NewReader([]byte("hello")),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		renewed, err := c.CreateGrant(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		transfer = grantS3(renewed.Endpoint, renewed.Region, renewed.Credentials)
		parts, err := transfer.ListParts(
			ctx,
			&s3.ListPartsInput{
				Bucket:   aws.String(second.Bucket),
				Key:      aws.String(second.ObjectKey),
				UploadId: multipart.UploadId,
			},
		)
		if err != nil || len(parts.Parts) != 1 {
			t.Fatalf("renewed multipart resume: %v", err)
		}
		if _, err = transfer.CompleteMultipartUpload(
			ctx,
			&s3.CompleteMultipartUploadInput{
				Bucket:   aws.String(second.Bucket),
				Key:      aws.String(second.ObjectKey),
				UploadId: multipart.UploadId,
				MultipartUpload: &types.CompletedMultipartUpload{
					Parts: []types.CompletedPart{{PartNumber: aws.Int32(1), ETag: part.ETag}},
				},
			},
		); err != nil {
			t.Fatal(err)
		}
		complete, err := c.CompleteFiles(ctx, api.CompleteFilesRequest{one.FileID, two.FileID})
		if err != nil || complete[0].Error != nil || complete[1].Error != nil {
			t.Fatalf("complete: %v", err)
		}
		req.Scope = api.GrantScope{Preset: "download"}
		download, err := c.CreateGrant(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		reader := grantS3(download.Endpoint, download.Region, download.Credentials)
		for _, file := range download.Files {
			head, err := reader.HeadObject(
				ctx,
				&s3.HeadObjectInput{Bucket: aws.String(file.Bucket), Key: aws.String(file.ObjectKey)},
			)
			if err != nil || aws.ToInt64(head.ContentLength) != 5 {
				t.Fatalf("head: %v", err)
			}
			got, err := reader.GetObject(
				ctx,
				&s3.GetObjectInput{
					Bucket: aws.String(file.Bucket),
					Key:    aws.String(file.ObjectKey),
					Range:  aws.String("bytes=1-3"),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(got.Body)
			got.Body.Close()
			if err != nil || string(data) != "ell" {
				t.Fatalf("range: %q %v", data, err)
			}
		}
		if _, err = reader.PutObject(
			ctx,
			&s3.PutObjectInput{
				Bucket: aws.String(first.Bucket),
				Key:    aws.String(first.ObjectKey),
				Body:   bytes.NewReader([]byte("wrong")),
			},
		); err == nil {
			t.Fatal("download credential allowed PUT")
		}
	})
}

func grantS3(endpoint, region string, credential api.GrantCredentials) *s3.Client {
	return s3.New(
		s3.Options{
			Region:           region,
			BaseEndpoint:     aws.String(endpoint),
			UsePathStyle:     true,
			RetryMaxAttempts: 1,
			HTTPClient:       &http.Client{Timeout: 10 * time.Second},
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     credential.AccessKeyID,
					SecretAccessKey: credential.SecretAccessKey,
					SessionToken:    credential.SessionToken,
				}, nil
			}),
		},
	)
}
