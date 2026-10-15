package integration_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"

	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestStorageHandoffCombinedListener(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		driver := db.DriverPostgreSQL
		if database.Dialector.Name() == "mysql" {
			driver = db.DriverMySQL
		}
		storageDB := fixture.Open(t, driver)
		endpoint := httptest.NewServer(&storageFileS3{})
		defer endpoint.Close()
		reserved, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := reserved.Addr().String()
		reserved.Close()
		svc, err := service.New(t.Context(), database, service.Options{
			IdentityResolver:      resolve,
			AutoMigrate:           true,
			StorageDB:             storageDB,
			StorageAutoMigrate:    true,
			TenantServiceVerifier: func(*http.Request) error { return nil },
			Storage: &service.StorageConfig{
				BackendID:       "test",
				Endpoint:        endpoint.URL,
				Region:          "us-east-1",
				AccessKeyID:     "test",
				SecretAccessKey: "test",
				UsePathStyle:    true,
			},
			StorageInternal: &service.StorageInternalConfig{
				ListenAddress: address,
				Consumers: []service.StorageConsumerConfig{
					{
						ID:     "artifact",
						Tokens: []string{"old-token", "new-token"},
						Scopes: []service.StorageScopeConfig{{TenantID: "tenant"}},
					},
				},
			},
			StorageHandoff: &service.StorageHandoffConfig{
				Endpoint: "http://" + address,
				Token:    "new-token",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		manager, err := conf.NewManagerFromConfig(
			&conf.Config{AppName: "handoff-test", AppCode: 100, RunMode: conf.RunModeTest},
		)
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close(context.Background())
		log, err := logger.NewLogger(&logger.Config{Level: "error"})
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		h, err := host.NewHTTPHost(manager, log, nil, svc)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		public := httptest.NewServer(h.Handler())
		defer public.Close()
		if err = svc.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := svc.Stop(ctx); err != nil {
				t.Error(err)
			}
		})
		client := &http.Client{Timeout: time.Second}
		for _, token := range []string{"", "old-token", "new-token"} {
			req, _ := http.NewRequest(
				"POST",
				"http://"+address+"/internal/v1/storage/reference-files/query",
				strings.NewReader(
					`{"scope":{"tenantID":"tenant","projectID":null},"fileIDs":["absent"]}`,
				),
			)
			req.Header.Set("Content-Type", "application/json")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			want := 404
			if token == "" {
				want = 401
			}
			if res.StatusCode != want {
				t.Fatalf("internal status=%d want=%d", res.StatusCode, want)
			}
		}
		res, err := client.Post(
			public.URL+"/internal/v1/storage/references/bind",
			"application/json",
			strings.NewReader(`{}`),
		)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("internal route leaked into public listener: %d", res.StatusCode)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err = svc.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if res, err = client.Get("http://" + address); err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			t.Fatal("internal listener survived Stop")
		}
	})
}
