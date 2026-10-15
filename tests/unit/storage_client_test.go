package unit_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/storage/client"
)

func TestStorageBucketOwnershipPagination(t *testing.T) {
	const tenant = "t_0123456789abcdef01234567"
	const bucket = "tenant-t-0123456789abcdef01234567"
	pages := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		pages++
		switch r.URL.Query().Get("continuation-token") {
		case "":
			_, _ = io.WriteString(
				w,
				`<ListAllMyBucketsResult><Buckets><Bucket><Name>other</Name></Bucket></Buckets><ContinuationToken>next</ContinuationToken></ListAllMyBucketsResult>`,
			)
		case "next":
			_, _ = fmt.Fprintf(
				w,
				`<ListAllMyBucketsResult><Buckets><Bucket><Name>%s</Name></Bucket></Buckets></ListAllMyBucketsResult>`,
				bucket,
			)
		default:
			t.Error("unexpected pagination token")
		}
	}))
	defer endpoint.Close()
	c, err := client.New(
		client.Config{
			BackendID:       "fixture",
			Endpoint:        endpoint.URL,
			Region:          "us-east-1",
			AccessKeyID:     "access",
			SecretAccessKey: "secret",
			UsePathStyle:    true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, created, err := c.EnsureBucket(t.Context(), tenant)
	if err != nil || created || got != bucket || pages != 2 {
		t.Fatalf("bucket=%q created=%t pages=%d err=%v", got, created, pages, err)
	}
}

func TestStorageBucketCancellationRetainsCause(t *testing.T) {
	c, err := client.New(
		client.Config{
			BackendID:       "fixture",
			Endpoint:        "http://127.0.0.1:1",
			Region:          "us-east-1",
			AccessKeyID:     "access",
			SecretAccessKey: "secret",
			UsePathStyle:    true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = c.EnsureBucket(ctx, "t_0123456789abcdef01234567")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe backend details in message")
	}
}
