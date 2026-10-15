package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	frameworkerrors "github.com/FlyTOmeLight/server-go/errors"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

func storageClientFor(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *api.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := api.NewClient(api.ClientConfig{Endpoint: server.URL, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestStorageCreateTenantBucket(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client := storageClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/tenant-buckets" {
					t.Errorf("request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer service" ||
					r.Header.Get("X-Request-ID") != "request-1" {
					t.Error("missing request credentials")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 1 || body["tenantID"] != "t_0123456789abcdef01234567" {
					t.Errorf("body: %#v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(
					w,
					`{"tenantID":"t_0123456789abcdef01234567","backendID":"local","bucket":"tenant-t-0123456789abcdef01234567"}`,
				)
			}, 0)
			ctx := api.WithRequestIdentity(
				t.Context(),
				api.RequestIdentity{BearerToken: "service", RequestID: "request-1"},
			)
			got, err := client.CreateTenantBucket(
				ctx,
				&api.CreateTenantBucketRequest{TenantID: "t_0123456789abcdef01234567"},
			)
			if err != nil || got.TenantID != "t_0123456789abcdef01234567" || got.BackendID != "local" ||
				got.Bucket != "tenant-t-0123456789abcdef01234567" {
				t.Fatalf("response: %+v %v", got, err)
			}
		})
	}
}

func TestStorageCredentialsArePerRequest(t *testing.T) {
	client := storageClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		wantAuth := ""
		if requestID != "" {
			wantAuth = "Bearer " + requestID
		}
		if r.Header.Get("Authorization") != wantAuth || r.Header.Get("X-Tenant-ID") != "" ||
			r.Header.Get("X-Project-ID") != "" {
			t.Error("request credential contamination")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}, 0)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprintf("service-%d", i)
			ctx := api.WithRequestIdentity(
				t.Context(),
				api.RequestIdentity{BearerToken: token, RequestID: token},
			)
			if _, err := client.CreateTenantBucket(
				ctx,
				&api.CreateTenantBucketRequest{TenantID: "t_0123456789abcdef01234567"},
			); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if _, err := client.CreateTenantBucket(t.Context(), &api.CreateTenantBucketRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateTenantBucket(t.Context(), nil); err == nil {
		t.Fatal("nil request accepted")
	}
}

func TestStorageHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		body, contentType, code string
		status                  int
	}{
		{body: `{"code":"Forbidden","message":"denied","statusCode":403}`, contentType: "application/json", code: "Forbidden", status: 403},
		{body: "gateway unavailable", contentType: "text/plain", code: "HTTPError", status: 503},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var calls atomic.Int32
			client := storageClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}, 0)
			_, err := client.CreateTenantBucket(t.Context(), &api.CreateTenantBucketRequest{})
			var detail *frameworkerrors.Error
			if !errors.As(err, &detail) || detail.Status != tc.status || detail.Code != tc.code {
				t.Fatalf("error: %#v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d", calls.Load())
			}
		})
	}
}

func TestStorageCancellationAndTimeout(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			started := make(chan struct{})
			client := storageClientFor(
				t,
				func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					close(started)
					<-r.Context().Done()
				},
				100*time.Millisecond,
			)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := client.CreateTenantBucket(ctx, &api.CreateTenantBucketRequest{}); result <- err }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("request did not start")
			}
			want := context.DeadlineExceeded
			if cancelled {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("error %v want %v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("request did not stop")
			}
		})
	}
}
