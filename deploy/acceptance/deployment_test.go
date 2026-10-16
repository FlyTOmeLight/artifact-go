package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func payload(i int) []byte {
	data := make([]byte, 1<<20)
	// Reproducible, nonzero content with a different hash per object.
	for offset := 0; offset < len(data); offset += sha256.Size {
		sum := sha256.Sum256([]byte(fmt.Sprintf("acceptance/%d/%d", i, offset)))
		copy(data[offset:], sum[:])
	}
	return data
}

func TestJuiceFSBytes(t *testing.T) {
	endpoint := os.Getenv("ARTIFACT_ACCEPTANCE_S3")
	if os.Getenv("ARTIFACT_DEPLOY_ACCEPTANCE") != "1" || endpoint == "" {
		t.Skip("requires deploy/acceptance/run.py")
	}
	c := s3.New(
		s3.Options{
			Region:       "us-east-1",
			BaseEndpoint: aws.String(endpoint),
			UsePathStyle: true,
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     os.Getenv("MINIO_ROOT_USER"),
					SecretAccessKey: os.Getenv("MINIO_ROOT_PASSWORD"),
				}, nil
			}),
			HTTPClient:       &http.Client{Timeout: 10 * time.Second},
			RetryMaxAttempts: 1,
		},
	)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	write := os.Getenv("ARTIFACT_ACCEPTANCE_WRITE") == "1"
	bucket := "acceptance-bytes"
	if write {
		if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket}); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	slots := make(chan struct{}, 4)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			data := payload(i)
			key := fmt.Sprintf("data/%02d.bin", i)
			if write {
				_, err := c.PutObject(
					ctx,
					&s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: bytes.NewReader(data)},
				)
				if err != nil {
					results <- err
					return
				}
			}
			out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
			if err != nil {
				results <- err
				return
			}
			got, err := io.ReadAll(out.Body)
			out.Body.Close()
			if err != nil {
				results <- err
				return
			}
			if sha256.Sum256(got) != sha256.Sum256(data) {
				results <- fmt.Errorf("hash mismatch for %s", key)
			}
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		t.Error(err)
	}
	t.Logf("write=%v objects=8 bytes=8388608 concurrency=4 elapsed=%s", write, time.Since(started))
}
