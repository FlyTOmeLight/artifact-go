package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"
	"gorm.io/gorm"

	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	h "github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestInternalHandoffShutdownJoinsDatabaseHandler(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		backend := httptest.NewServer(&storageFileS3{})
		defer backend.Close()
		address := referenceAddress(t)
		svc, err := service.NewStorage(
			t.Context(),
			database,
			service.StorageOptions{
				Config: &service.StorageConfig{
					BackendID:       "fixture",
					Endpoint:        backend.URL,
					Region:          "us-east-1",
					AccessKeyID:     "fixture",
					SecretAccessKey: "fixture-secret",
					UsePathStyle:    true,
				},
				IdentityResolver:      resolve,
				TenantServiceVerifier: func(*http.Request) error { return nil },
				AutoMigrate:           true,
				Internal: &service.StorageInternalConfig{
					ListenAddress: address,
					Consumers: []service.StorageConsumerConfig{
						{
							ID:     "consumer",
							Tokens: []string{"token"},
							Scopes: []service.StorageScopeConfig{{TenantID: "tenant"}},
						},
					},
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		size := int64(1)
		file := st.StorageFile{
			ID:           uuid.NewString(),
			TenantID:     "tenant",
			ScopeKey:     "tenant",
			CreatedBy:    "user",
			RequestKey:   "shutdown",
			RequestHash:  "hash",
			BackendID:    "fixture",
			Bucket:       "bucket",
			ObjectKey:    "key",
			Size:         &size,
			ExpectedSize: 1,
			ContentType:  "application/octet-stream",
			State:        "ready",
			ExpiresAt:    time.Now().UTC().Add(time.Hour),
		}
		if err := database.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
		entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		defer unblock()
		name := "test:handoff-shutdown"
		if err := database.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
			row, ok := tx.Statement.Dest.(*st.StorageFile)
			if !ok || row.ID != file.ID {
				return
			}
			close(entered)
			<-tx.Statement.Context.Done()
			close(canceled)
			<-release
		}); err != nil {
			t.Fatal(err)
		}
		defer database.Callback().Query().Remove(name)
		if err := svc.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		stopped := false
		defer func() {
			unblock()
			if !stopped {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = svc.Stop(ctx)
			}
		}()
		c, err := h.NewClient(h.ClientConfig{Endpoint: "http://" + address, Token: "token"})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		requestDone := make(chan error, 1)
		go func() {
			_, err := c.Metadata(context.Background(), h.Scope{TenantID: "tenant"}, []string{file.ID})
			requestDone <- err
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("handler did not enter database callback")
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		stopDone := make(chan error, 1)
		go func() { stopDone <- svc.Stop(stopCtx) }()
		select {
		case <-canceled:
		case <-time.After(5 * time.Second):
			t.Fatal("Stop did not cancel active request")
		}
		select {
		case err := <-stopDone:
			stopped = true
			t.Fatalf("Stop returned before handler exited: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		// Release the request's final database work after observing that Stop is still waiting.
		unblock()
		select {
		case err := <-stopDone:
			stopped = true
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost shutdown deadline: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Stop did not join completed handler")
		}
		select {
		case err := <-requestDone:
			if err == nil {
				t.Fatal("canceled request unexpectedly succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("request did not stop")
		}
	})
}
