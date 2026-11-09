package unit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	client "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
)

func TestStorageAdapterCancellationRetainsCause(t *testing.T) {
	backend, err := client.New(
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
	for _, check := range []struct {
		name string
		call func(context.Context) error
	}{
		{name: "check_bucket", call: func(ctx context.Context) error {
			return backend.CheckBucket(ctx, "bucket")
		}},
		{name: "stat", call: func(ctx context.Context) error {
			_, err := backend.Stat(ctx, "bucket", "key")
			return err
		}},
		{name: "delete", call: func(ctx context.Context) error {
			return backend.Delete(ctx, "bucket", "key")
		}},
		{name: "incomplete_uploads", call: func(ctx context.Context) error {
			_, err := backend.HasIncompleteUploads(ctx, "bucket", "key")
			return err
		}},
	} {
		t.Run(check.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := check.call(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
			if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe backend details in message")
			}
		})
	}
}
