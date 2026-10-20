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

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	serr "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"

	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// This endpoint models S3 ownership separately from public HEAD accessibility.
type tenantBucketS3 struct {
	mu      sync.Mutex
	buckets map[string]bool
	calls   atomic.Int64
	mode    string
}

func (s *tenantBucketS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	if s.mode == "denied" {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code></Error>`)
		return
	}
	bucket := strings.Trim(r.URL.Path, "/")
	switch r.Method {
	case http.MethodHead:
		if s.mode == "foreign" || s.buckets[bucket] {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(404)
		}
	case http.MethodPut:
		if s.mode == "conflict" {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `<Error><Code>BucketAlreadyExists</Code></Error>`)
			return
		}
		if s.buckets[bucket] {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `<Error><Code>BucketAlreadyOwnedByYou</Code></Error>`)
			return
		}
		s.buckets[bucket] = true
		w.WriteHeader(200)
	case http.MethodGet:
		_, _ = io.WriteString(w, `<ListAllMyBucketsResult><Owner><ID>fixture-owner</ID></Owner><Buckets>`)
		for name := range s.buckets {
			_, _ = fmt.Fprintf(w, `<Bucket><Name>%s</Name></Bucket>`, name)
		}
		_, _ = io.WriteString(w, `</Buckets></ListAllMyBucketsResult>`)
	default:
		w.WriteHeader(405)
	}
}

func tenantBucketHTTP(t *testing.T, database *db.DB, options service.Options) *httptest.Server {
	t.Helper()
	options.AutoMigrate = true
	svc, err := service.New(t.Context(), database, options)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManagerFromConfig(
		&conf.Config{AppName: "tenant-bucket-test", AppCode: 100, RunMode: conf.RunModeTest},
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
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(h.Handler())
	t.Cleanup(server.Close)
	return server
}

func tenantBucketRequest(ctx context.Context, server *httptest.Server, token, body string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL+"/v1/storage/tenant-buckets",
		strings.NewReader(body),
	)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-forge-tenant-id", tenantA)
	req.Header.Set("X-User-ID", "admin")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	return response.StatusCode, data, err
}

func TestTenantBucketManagement(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		backend := &tenantBucketS3{buckets: make(map[string]bool)}
		s3 := httptest.NewServer(backend)
		t.Cleanup(s3.Close)
		cfg := &service.StorageConfig{
			BackendID:       "fixture",
			Endpoint:        s3.URL,
			Region:          "us-east-1",
			AccessKeyID:     "fixture-key",
			SecretAccessKey: "fixture-secret",
			UsePathStyle:    true,
		}
		var userCalls atomic.Int64
		options := service.Options{
			IdentityResolver: func(r *http.Request) (service.RequestIdentity, error) { userCalls.Add(1); return resolve(r) },
			Storage:          cfg,
			TenantServiceVerifier: func(r *http.Request) error {
				if r.Header.Get("Authorization") != "Bearer tenant-service" {
					return serr.NewError(403, "Forbidden", "tenant service required")
				}
				return nil
			},
		}
		server := tenantBucketHTTP(t, database, options)
		body := fmt.Sprintf(`{"tenantID":%q}`, tenantA)
		for _, token := range []string{"", "tenant", "project"} {
			status, data, err := tenantBucketRequest(t.Context(), server, token, body)
			if err != nil || status != 403 {
				t.Fatalf("ordinary user %q status=%d data=%s err=%v", token, status, data, err)
			}
		}
		if backend.calls.Load() != 0 {
			t.Fatal("rejected users reached S3")
		}
		for _, tc := range []struct{ name, body string }{
			{name: "null", body: `null`},
			{name: "empty", body: `{}`},
			{name: "array", body: `[]`},
			{name: "malformed", body: `{`},
			{name: "multiple", body: body + ` {}`},
			{name: "upper", body: `{"tenantID":"T_000000000000000000000001"}`},
			{name: "nonhex", body: `{"tenantID":"t_00000000000000000000000g"}`},
			{name: "short", body: `{"tenantID":"t_01"}`},
			{name: "bucket", body: `{"tenantID":"t_000000000000000000000001","bucket":"other"}`},
			{name: "backend", body: `{"tenantID":"t_000000000000000000000001","backendID":"other"}`},
			{name: "path", body: `{"tenantID":"t_000000000000000000000001","path":"other"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				status, data, err := tenantBucketRequest(t.Context(), server, "tenant-service", tc.body)
				if err != nil || status != 400 {
					t.Fatalf("status=%d data=%s err=%v", status, data, err)
				}
			})
		}
		if backend.calls.Load() != 0 {
			t.Fatal("invalid requests reached S3")
		}
		for _, want := range []int{201, 200} {
			status, data, err := tenantBucketRequest(t.Context(), server, "tenant-service", body)
			if err != nil || status != want {
				t.Fatalf("status=%d want=%d data=%s err=%v", status, want, data, err)
			}
			var out map[string]string
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			if len(out) != 3 || out["tenantID"] != tenantA || out["backendID"] != "fixture" ||
				out["bucket"] != "tenant-t-000000000000000000000001" {
				t.Fatalf("response=%s", data)
			}
		}
		type result struct {
			status int
			data   []byte
			err    error
		}
		results := make(chan result, 8)
		start := make(chan struct{})
		for range 8 {
			go func() {
				<-start
				status, data, err := tenantBucketRequest(
					t.Context(),
					server,
					"tenant-service",
					`{"tenantID":"t_000000000000000000000002"}`,
				)
				results <- result{status: status, data: data, err: err}
			}()
		}
		close(start)
		created := 0
		for range 8 {
			r := <-results
			if r.err != nil || (r.status != 201 && r.status != 200) {
				t.Fatalf("concurrent=%+v body=%s", r, r.data)
			}
			if r.status == 201 {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("created=%d", created)
		}
		if userCalls.Load() != 0 {
			t.Fatal("management route invoked ordinary identity resolver")
		}
		backend.mu.Lock()
		count := len(backend.buckets)
		backend.mu.Unlock()
		if count != 2 {
			t.Fatalf("created %d buckets", count)
		}
		// Enabling storage never replaces ordinary artifact authorization.
		response, err := server.Client().Get(server.URL + "/v1/artifacts")
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 401 || userCalls.Load() != 1 {
			t.Fatalf("artifact auth status=%d calls=%d", response.StatusCode, userCalls.Load())
		}
		for _, mode := range []string{"denied", "foreign", "conflict"} {
			backend.mu.Lock()
			backend.mode = mode
			backend.mu.Unlock()
			status, data, err := tenantBucketRequest(
				t.Context(),
				server,
				"tenant-service",
				`{"tenantID":"t_000000000000000000000003"}`,
			)
			wantStatus, wantCode := 409, "StorageBucketConflict"
			if mode == "denied" {
				wantStatus, wantCode = 503, "StorageBucketUnavailable"
			}
			if err != nil || status != wantStatus || !strings.Contains(string(data), wantCode) {
				t.Fatalf("%s status=%d data=%s err=%v", mode, status, data, err)
			}
			if strings.Contains(string(data), cfg.SecretAccessKey) ||
				strings.Contains(string(data), cfg.AccessKeyID) {
				t.Fatal("response disclosed S3 credentials")
			}
		}
		missing := options
		missing.TenantServiceVerifier = nil
		if _, err := service.New(t.Context(), database, missing); err == nil {
			t.Fatal("enabled management without verifier")
		}
		disabled := options
		disabled.Storage = nil
		disabled.TenantServiceVerifier = nil
		disabledServer := tenantBucketHTTP(t, database, disabled)
		status, _, err := tenantBucketRequest(t.Context(), disabledServer, "tenant-service", body)
		if err != nil || status != 404 {
			t.Fatalf("disabled management status=%d err=%v", status, err)
		}
	})
}
