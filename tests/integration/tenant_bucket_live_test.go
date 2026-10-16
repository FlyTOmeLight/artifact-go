package integration_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	client "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
)

func TestTenantBucketLive(t *testing.T) {
	if os.Getenv("ARTIFACT_TEST_S3_ACCESS_KEY_ID") == "" {
		t.Skip("live credentials required")
	}
	cfg := client.Config{
		BackendID:       "juicefs-local",
		Endpoint:        os.Getenv("ARTIFACT_TEST_S3_ENDPOINT"),
		Region:          "us-east-1",
		AccessKeyID:     os.Getenv("ARTIFACT_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("ARTIFACT_TEST_S3_SECRET_ACCESS_KEY"),
		UsePathStyle:    true,
	}
	c, err := client.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cleanupClient := s3.New(
		s3.Options{
			HTTPClient:       &http.Client{Timeout: 5 * time.Second},
			RetryMaxAttempts: 1,
			Region:           cfg.Region,
			BaseEndpoint:     aws.String(cfg.Endpoint),
			UsePathStyle:     true,
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     cfg.AccessKeyID,
					SecretAccessKey: os.Getenv("ARTIFACT_TEST_S3_SECRET_ACCESS_KEY"),
				}, nil
			}),
		},
	)
	tenant := fmt.Sprintf("t_%x", bucketRandom(t))
	bucket, _ := client.TenantBucket(tenant)
	cleanup := func(bucket string) {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, e := cleanupClient.DeleteBucket(
				ctx,
				&s3.DeleteBucketInput{Bucket: aws.String(bucket)},
			); e != nil {
				t.Error("cleanup failed")
				return
			}
			_, e := cleanupClient.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
			var api smithy.APIError
			if !errors.As(e, &api) || (api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchBucket") {
				t.Error("deleted probe bucket still present or cannot confirm absence")
			}
		})
	}
	cleanup(bucket)
	got, created, err := c.EnsureBucket(context.Background(), tenant)
	if err != nil || !created || got != bucket {
		t.Fatalf("new: %v %v", created, err)
	}
	_, created, err = c.EnsureBucket(context.Background(), tenant)
	if err != nil || created {
		t.Fatalf("repeat: %v %v", created, err)
	}
	cfg.SecretAccessKey = "invalid-probe-secret"
	bad, _ := client.New(cfg)
	if _, _, err = bad.EnsureBucket(context.Background(), tenant); err == nil {
		t.Fatal("invalid credential accepted")
	}
	tenant2 := fmt.Sprintf("t_%x", bucketRandom(t))
	bucket2, _ := client.TenantBucket(tenant2)
	cleanup(bucket2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	news := 0
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			got, new, e := c.EnsureBucket(context.Background(), tenant2)
			if e != nil || got != bucket2 {
				t.Errorf("concurrent: %v", e)
			}
			if new {
				mu.Lock()
				news++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if news != 1 {
		t.Fatalf("expected exactly one creator, got %d", news)
	}
	t.Logf("concurrent created=%d existing=%d", news, 8-news)
}

func bucketRandom(t *testing.T) []byte {
	t.Helper()
	v := make([]byte, 12)
	if _, e := rand.Read(v); e != nil {
		t.Fatal(e)
	}
	return v
}
