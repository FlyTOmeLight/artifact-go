package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/FlyTOmeLight/artifact-go/internal/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	h "github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// Run only against the disposable resources created by run.py.
func TestTLSRotation(t *testing.T) {
	if os.Getenv("ARTIFACT_DEPLOY_ACCEPTANCE") != "1" {
		t.Skip("requires deploy/acceptance/run.py")
	}
	database := fixture.Open(t, db.DriverPostgreSQL)
	if err := st.Migrate(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	size := int64(7)
	if err := database.Create(
		&st.StorageFile{
			ID:           "file",
			TenantID:     "tenant",
			ScopeKey:     "tenant",
			CreatedBy:    "operator",
			RequestKey:   "file",
			RequestHash:  "file",
			BackendID:    "test",
			Bucket:       "test",
			ObjectKey:    "file",
			ExpectedSize: 7,
			Size:         &size,
			ContentType:  "text/plain",
			State:        "ready",
			ExpiresAt:    time.Now().UTC().Add(time.Hour),
		},
	).Error; err != nil {
		t.Fatal(err)
	}
	scope := h.Scope{TenantID: "tenant"}
	refs := []h.Reference{{ReferenceID: "reference", FileID: "file"}}
	start := func(tokens ...string) *httptest.Server {
		handler, err := h.NewHandler(
			storage.NewBindingService(database),
			[]h.Consumer{{ID: "artifact", Tokens: tokens, Scopes: []h.Scope{scope}}},
		)
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewTLSServer(handler)
	}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	client := func(endpoint, token string) *h.Client {
		c, err := h.NewClient(h.ClientConfig{Endpoint: endpoint, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		return c
	}
	first := start("old")
	defer first.Close()
	_, err := client(first.URL, "old").Statuses(t.Context(), scope, refs)
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("untrusted CA must fail verification: %v", err)
	}
	trust := func(s *httptest.Server) {
		pool := x509.NewCertPool()
		pool.AddCert(s.Certificate())
		tr := oldTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
		http.DefaultTransport = tr
		t.Cleanup(tr.CloseIdleConnections)
	}
	trust(first)
	if _, err = client(first.URL, "old").Bind(t.Context(), scope, refs); err != nil {
		t.Fatal(err)
	}
	first.Close()
	overlap := start("old", "new")
	defer overlap.Close()
	trust(overlap)
	for _, token := range []string{"old", "new"} {
		status, err := client(overlap.URL, token).Statuses(t.Context(), scope, refs)
		if err != nil || len(status) != 1 || status[0].State != "active" {
			t.Fatalf("overlap lost reference: %v %v", status, err)
		}
	}
	overlap.Close()
	final := start("new")
	defer final.Close()
	trust(final)
	_, err = client(final.URL, "old").Statuses(t.Context(), scope, refs)
	var denied *h.Error
	if !errors.As(err, &denied) || denied.Status != 401 {
		t.Fatalf("old token must be revoked: %v", err)
	}
	current := client(final.URL, "new")
	_, err = current.Statuses(t.Context(), h.Scope{TenantID: "other-tenant"}, refs)
	if !errors.As(err, &denied) || denied.Status != 403 {
		t.Fatalf("token must remain restricted to its exact scope: %v", err)
	}
	status, err := current.Statuses(t.Context(), scope, refs)
	if err != nil || status[0].State != "active" {
		t.Fatalf("ownership lost: %v %v", status, err)
	}
	if _, err = current.Release(t.Context(), scope, refs); err != nil {
		t.Fatal(err)
	}
	t.Log(
		"TLS verified; unknown CA rejected; old -> overlap -> new-only preserves ConsumerID/reference; old token rejected 401; new token releases",
	)
}

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
