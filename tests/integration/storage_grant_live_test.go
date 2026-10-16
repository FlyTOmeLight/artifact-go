package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	backend "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
)

func TestStorageGrantAdapterLive(t *testing.T) {
	if os.Getenv("ARTIFACT_TEST_STS_ACCESS_KEY_ID") == "" {
		t.Skip("live ordinary IAM issuer required")
	}
	cfg := backend.Config{
		BackendID:       "juicefs",
		Endpoint:        os.Getenv("ARTIFACT_TEST_S3_ENDPOINT"),
		Region:          "us-east-1",
		AccessKeyID:     os.Getenv("ARTIFACT_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("ARTIFACT_TEST_S3_SECRET_ACCESS_KEY"),
		UsePathStyle:    true,
		GrantIssuer: &backend.GrantIssuerConfig{
			Endpoint:        os.Getenv("ARTIFACT_TEST_S3_ENDPOINT"),
			AccessKeyID:     os.Getenv("ARTIFACT_TEST_STS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("ARTIFACT_TEST_STS_SECRET_ACCESS_KEY"),
		},
	}
	adapter, err := backend.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s3client := func(access, secret, token string) *s3.Client {
		return s3.New(
			s3.Options{
				Region:           "us-east-1",
				BaseEndpoint:     aws.String(cfg.Endpoint),
				UsePathStyle:     true,
				RetryMaxAttempts: 1,
				HTTPClient:       &http.Client{Timeout: 5 * time.Second},
				Credentials: aws.CredentialsProviderFunc(
					func(context.Context) (aws.Credentials, error) {
						return aws.Credentials{
							AccessKeyID:     access,
							SecretAccessKey: secret,
							SessionToken:    token,
						}, nil
					},
				),
			},
		)
	}
	operator := s3client(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	tenant := fmt.Sprintf("t_%x", bucketRandom(t))
	bucket, _, err := adapter.EnsureBucket(t.Context(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := adapter.EnsureBucket(t.Context(), fmt.Sprintf("t_%x", bucketRandom(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated test buckets: %s %s", bucket, other)
	t.Cleanup(func() {
		for _, b := range []string{bucket, other} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			for _, key := range []string{"one", "two", "outside"} {
				_ = adapter.Delete(ctx, b, key)
			}
			_, e := operator.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(b)})
			cancel()
			if e != nil {
				t.Error("cleanup bucket failed")
			}
		}
	})
	grant := func(actions, keys []string) (*s3.Client, time.Time) {
		credentials, e := adapter.CreateGrant(
			t.Context(),
			backend.GrantRequest{
				Bucket:   bucket,
				Keys:     keys,
				Actions:  actions,
				Deadline: time.Now().Add(907 * time.Second),
			},
		)
		if e != nil {
			t.Fatal(e)
		}
		return s3client(
			credentials.AccessKeyID,
			credentials.SecretAccessKey,
			credentials.SessionToken,
		), credentials.ExpiresAt
	}
	upload, expiry := grant(
		[]string{"s3:PutObject", "s3:ListMultipartUploadParts", "s3:AbortMultipartUpload"},
		[]string{"one", "two"},
	)
	put := func(c *s3.Client, b, key string) error {
		_, e := c.PutObject(
			t.Context(),
			&s3.PutObjectInput{
				Bucket: aws.String(b),
				Key:    aws.String(key),
				Body:   bytes.NewReader([]byte("hello")),
			},
		)
		return e
	}
	for _, key := range []string{"one", "two"} {
		if e := put(upload, bucket, key); e != nil {
			t.Fatal("allowed put failed")
		}
	}
	denied := func(err error) {
		t.Helper()
		var api smithy.APIError
		if !errors.As(err, &api) || api.ErrorCode() != "AccessDenied" {
			t.Fatalf("expected AccessDenied, got %T", err)
		}
	}
	denied(put(upload, bucket, "outside"))
	denied(put(upload, other, "one"))
	_, readErr := upload.GetObject(
		t.Context(),
		&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("one")},
	)
	denied(readErr)
	multipart, e := upload.CreateMultipartUpload(
		t.Context(),
		&s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("two")},
	)
	if e != nil {
		t.Fatal("multipart create")
	}
	_, e = upload.UploadPart(
		t.Context(),
		&s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String("two"),
			UploadId:   multipart.UploadId,
			PartNumber: aws.Int32(1),
			Body:       bytes.NewReader([]byte("part")),
		},
	)
	if e != nil {
		t.Fatal("multipart upload")
	}
	_, e = upload.ListParts(
		t.Context(),
		&s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String("two"), UploadId: multipart.UploadId},
	)
	if e != nil {
		t.Fatal("list parts")
	}
	_, e = upload.AbortMultipartUpload(
		t.Context(),
		&s3.AbortMultipartUploadInput{
			Bucket:   aws.String(bucket),
			Key:      aws.String("two"),
			UploadId: multipart.UploadId,
		},
	)
	if e != nil {
		t.Fatal("abort")
	}
	download, _ := grant([]string{"s3:GetObject"}, []string{"one", "two"})
	if _, e = download.HeadObject(
		t.Context(),
		&s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("one")},
	); e != nil {
		t.Fatal("head")
	}
	response, e := download.GetObject(
		t.Context(),
		&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("one"), Range: aws.String("bytes=1-3")},
	)
	if e != nil {
		t.Fatal("range")
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "ell" {
		t.Fatal("range content")
	}
	denied(put(download, bucket, "one"))
	custom, _ := grant([]string{"s3:PutObject"}, []string{"one"})
	if put(custom, bucket, "one") != nil {
		t.Fatal("custom put")
	}
	_, e = custom.ListParts(
		t.Context(),
		&s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String("one"), UploadId: aws.String("unknown")},
	)
	denied(e)
	t.Logf("scope/multipart/read checks passed; expiration %s", expiry.UTC().Format(time.RFC3339))
	if os.Getenv("ARTIFACT_TEST_STS_EXPIRY") != "1" {
		return
	}
	timer := time.NewTimer(time.Until(expiry.Add(-10 * time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("cancelled")
	}
	if put(upload, bucket, "one") != nil {
		t.Fatal("pre-expiry positive control")
	}
	timer.Reset(time.Until(expiry.Add(2 * time.Second)))
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("cancelled")
	}
	expiredErr := put(upload, bucket, "one")
	var expiredAPI smithy.APIError
	if !errors.As(expiredErr, &expiredAPI) ||
		(expiredAPI.ErrorCode() != "InvalidAccessKeyId" && expiredAPI.ErrorCode() != "ExpiredToken" && expiredAPI.ErrorCode() != "InvalidToken" && expiredAPI.ErrorCode() != "AccessDenied") {
		t.Fatalf("unexpected expired credential result: %T", expiredErr)
	}
	t.Logf("expired credential rejected: %s", expiredAPI.ErrorCode())
	if put(operator, bucket, "one") != nil {
		t.Fatal("post-expiry operator control")
	}
	t.Log("real expiry rejection passed")
}
