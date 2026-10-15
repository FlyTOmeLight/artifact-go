package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

type storageFileS3 struct {
	objectStatus atomic.Int32
	objectSize   atomic.Int64
	objectHeads  atomic.Int32
}

func (s *storageFileS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	if r.Method == "HEAD" {
		if strings.Contains(strings.Trim(r.URL.Path, "/"), "/") {
			s.objectHeads.Add(1)
			status := int(s.objectStatus.Load())
			if status == 0 {
				status = 200
			}
			if status == 200 {
				w.Header().Set("Content-Length", fmt.Sprint(s.objectSize.Load()))
			}
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(200)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/" {
		fmt.Fprint(
			w,
			`<ListAllMyBucketsResult><Owner><ID>fixture</ID></Owner><Buckets><Bucket><Name>tenant-t-000000000000000000000001</Name></Bucket><Bucket><Name>tenant-t-000000000000000000000002</Name></Bucket></Buckets></ListAllMyBucketsResult>`,
		)
		return
	}
	w.WriteHeader(405)
}

func storageFileHTTP(
	t *testing.T,
	database *db.DB,
	files *service.StorageFileConfig,
) (*api.Client, *httptest.Server, *storageFileS3) {
	t.Helper()
	backend := &storageFileS3{}
	s3 := httptest.NewServer(backend)
	t.Cleanup(s3.Close)
	client, server, _ := storageFileServer(t, database, service.StorageOptions{
		Config: &service.StorageConfig{
			BackendID:       "fixture",
			Endpoint:        s3.URL,
			Region:          "us-east-1",
			AccessKeyID:     "fixture",
			SecretAccessKey: "fixture-secret",
			UsePathStyle:    true,
		},
		IdentityResolver:      resolve,
		TenantServiceVerifier: func(*http.Request) error { return nil },
		AutoMigrate:           true,
		Files:                 files,
	})
	return client, server, backend
}

func storageFileServer(
	t *testing.T,
	database *db.DB,
	options service.StorageOptions,
) (*api.Client, *httptest.Server, *service.StorageService) {
	t.Helper()
	svc, err := service.NewStorage(t.Context(), database, options)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManagerFromConfig(
		&conf.Config{AppName: "storage-file-test", AppCode: 100, RunMode: conf.RunModeTest},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	log, err := logger.NewLogger(&logger.Config{Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	h, err := host.NewHTTPHost(manager, log, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	server := httptest.NewServer(h.Handler())
	t.Cleanup(server.Close)
	client, err := api.NewClient(api.ClientConfig{Endpoint: server.URL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, server, svc
}

func storageIdentity(t *testing.T, token string) context.Context {
	t.Helper()
	return api.WithRequestIdentity(t.Context(), api.RequestIdentity{BearerToken: token, TenantID: tenantA})
}

func storageRegister(t *testing.T, client *api.Client, ctx context.Context, key string, size int64) *api.FileResponse {
	t.Helper()
	result, err := client.RegisterFiles(ctx, api.RegisterFilesRequest{{RequestKey: key, Size: &size}})
	if err != nil || len(result) != 1 || result[0].Error != nil || result[0].FileResponse == nil {
		t.Fatalf("register: %+v %v", result, err)
	}
	return result[0].FileResponse
}

func TestStorageFileLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		client, _, backend := storageFileHTTP(t, database, nil)
		ctx := storageIdentity(t, "tenant")
		f := storageRegister(t, client, ctx, "one", 5)
		if f.State != api.FileUploading || f.Size != nil || f.ExpectedSize != 5 || f.DeclaredChecksum != nil ||
			f.ContentType != "application/octet-stream" {
			t.Fatalf("file: %+v", f)
		}
		again := storageRegister(t, client, ctx, "one", 5)
		if again.FileID != f.FileID || !again.ExpiresAt.Equal(f.ExpiresAt) {
			t.Fatalf("replay changed identity or expiry: %+v", again)
		}
		six := int64(6)
		results, err := client.RegisterFiles(
			ctx,
			api.RegisterFilesRequest{{RequestKey: "one", Size: &six}, {RequestKey: "two", Size: &six}},
		)
		if err != nil || len(results) != 2 || results[0].Error == nil || results[0].Error.Status != 409 ||
			results[1].Error != nil {
			t.Fatalf("partial: %+v %v", results, err)
		}
		for _, token := range []string{"project", "other-tenant"} {
			_, err := client.GetFile(storageIdentity(t, token), f.FileID)
			expectError(t, err, 404, "")
		}
		backend.objectStatus.Store(404)
		confirmed, err := client.CompleteFiles(ctx, api.CompleteFilesRequest{f.FileID})
		if err != nil || confirmed[0].Error == nil {
			t.Fatalf("missing: %+v %v", confirmed, err)
		}
		backend.objectStatus.Store(200)
		backend.objectSize.Store(4)
		confirmed, err = client.CompleteFiles(ctx, api.CompleteFilesRequest{f.FileID})
		if err != nil || confirmed[0].Error == nil {
			t.Fatalf("size mismatch: %+v %v", confirmed, err)
		}
		got, err := client.GetFile(ctx, f.FileID)
		if err != nil || got.State != api.FileUploading {
			t.Fatalf("failed complete changed state: %+v %v", got, err)
		}
		backend.objectSize.Store(5)
		confirmed, err = client.CompleteFiles(ctx, api.CompleteFilesRequest{f.FileID, "missing"})
		if err != nil || confirmed[0].Error != nil || confirmed[0].State != api.FileReady ||
			confirmed[0].Size == nil ||
			*confirmed[0].Size != 5 ||
			confirmed[1].Error == nil ||
			confirmed[1].Error.Status != 404 {
			t.Fatalf("complete: %+v %v", confirmed, err)
		}
		count := backend.objectHeads.Load()
		backend.objectStatus.Store(503)
		confirmed, err = client.CompleteFiles(ctx, api.CompleteFilesRequest{f.FileID})
		if err != nil || confirmed[0].Error != nil || backend.objectHeads.Load() != count {
			t.Fatalf("ready replay stats object: %+v %v", confirmed, err)
		}
		deleted, err := client.DeleteFile(ctx, f.FileID)
		if err != nil || deleted.State != api.FileDeleting {
			t.Fatalf("delete: %+v %v", deleted, err)
		}
		deleted, err = client.DeleteFile(ctx, f.FileID)
		if err != nil || deleted.State != api.FileDeleting {
			t.Fatalf("repeat delete: %+v %v", deleted, err)
		}
		replay := storageRegister(t, client, storageIdentity(t, "historical"), "one", 5)
		if replay.FileID != f.FileID || replay.State != api.FileDeleting {
			t.Fatalf("terminal replay: %+v", replay)
		}
		confirmed, err = client.CompleteFiles(ctx, api.CompleteFilesRequest{f.FileID})
		if err != nil || confirmed[0].Error == nil || confirmed[0].Error.Status != 409 {
			t.Fatalf("resurrection: %+v %v", confirmed, err)
		}
	})
}

func TestStorageFileBatchStructureAndFields(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		client, server, _ := storageFileHTTP(t, database, nil)
		for _, route := range []string{"/v1/storage/files", "/v1/storage/files/complete"} {
			bodies := []string{"", `null`, `{}`, `{"files":[]}`, `[]`, `[null]`, `[true]`}
			if strings.HasSuffix(route, "complete") {
				bodies = append(bodies, `[{}]`, `["a","a"]`)
			} else {
				bodies = append(
					bodies,
					`["a"]`,
					`[{"requestKey":"a","size":0},{"requestKey":"a","size":0}]`,
				)
			}
			for _, body := range bodies {
				req, _ := http.NewRequestWithContext(
					t.Context(),
					"POST",
					server.URL+route,
					strings.NewReader(body),
				)
				req.Header.Set("Authorization", "Bearer tenant")
				req.Header.Set("Content-Type", "application/json")
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode != 400 {
					t.Fatalf(
						"%s body=%s status=%d response=%s",
						route,
						body,
						response.StatusCode,
						data,
					)
				}
			}
		}
		zero := int64(0)
		negative := int64(-1)
		result, err := client.RegisterFiles(
			storageIdentity(t, "tenant"),
			api.RegisterFilesRequest{
				{RequestKey: "zero", Size: &zero},
				{RequestKey: "missing"},
				{RequestKey: "negative", Size: &negative},
				{
					RequestKey:       "checksum",
					Size:             &zero,
					DeclaredChecksum: &api.FileChecksum{Algorithm: "SHA256"},
				},
			},
		)
		if err != nil || len(result) != 4 || result[0].Error != nil {
			t.Fatalf("results %+v %v", result, err)
		}
		for _, item := range result[1:] {
			if item.Error == nil || item.Error.Status != 400 {
				t.Fatalf("invalid fields accepted: %+v", item)
			}
		}
		data, _ := json.Marshal(result)
		var values []map[string]json.RawMessage
		_ = json.Unmarshal(data, &values)
		for _, item := range values[1:] {
			if len(item) != 2 {
				t.Fatalf("failed item leaked file fields: %s", data)
			}
		}
		_, err = client.GetFile(t.Context(), result[0].FileID)
		expectError(t, err, 401, "")
	})
}

func TestStorageConcurrentRegistration(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		client, _, _ := storageFileHTTP(t, database, nil)
		const n = 8
		ids := make(chan string, n)
		errs := make(chan error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				size := int64(0)
				result, err := client.RegisterFiles(
					storageIdentity(t, "tenant"),
					api.RegisterFilesRequest{{RequestKey: "concurrent", Size: &size}},
				)
				if err != nil {
					errs <- err
					return
				}
				if result[0].Error != nil {
					errs <- fmt.Errorf("%+v", result[0].Error)
					return
				}
				ids <- result[0].FileID
			}()
		}
		wg.Wait()
		close(ids)
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		first := ""
		count := 0
		for id := range ids {
			count++
			if first == "" {
				first = id
			}
			if id != first {
				t.Errorf("distinct file IDs %s %s", first, id)
			}
		}
		if count != n {
			t.Fatalf("success count %d", count)
		}
	})
}

func TestStorageStandaloneDatabaseOwnership(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		backend := httptest.NewServer(&storageFileS3{})
		defer backend.Close()
		_, _, svc := storageFileServer(
			t,
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
			},
		)
		for _, name := range []string{"storage_files", "storage_tasks"} {
			if !database.Migrator().HasTable(name) {
				t.Errorf("missing storage table %s", name)
			}
		}
		for _, name := range []string{"artifacts", "versions", "drafts", "background_tasks", "reference_changes"} {
			if database.Migrator().HasTable(name) {
				t.Errorf("storage created artifact table %s", name)
			}
		}
		if err := svc.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := svc.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if err := database.Exec("SELECT 1").Error; err != nil {
			t.Fatalf("Stop closed borrowed database: %v", err)
		}
	})
}
