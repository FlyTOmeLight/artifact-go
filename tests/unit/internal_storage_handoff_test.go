package unit_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	h "github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
)

func TestInternalStorageClientBoundary(t *testing.T) {
	for _, endpoint := range []string{"http://example.com", "https://a/path", "https://user:pass@a", "https://a?query=x"} {
		if _, err := h.NewClient(h.ClientConfig{Endpoint: endpoint, Token: "secret"}); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer private" {
			t.Error("missing service identity")
		}
		w.WriteHeader(503)
		fmt.Fprint(w, `{"code":"StorageOutcomeUnknown"}`)
	}))
	defer server.Close()
	c, err := h.NewClient(h.ClientConfig{Endpoint: server.URL, Token: "private"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err = c.Release(ctx, h.Scope{TenantID: "tenant"}, []h.Reference{{ReferenceID: "r", FileID: "f"}})
	var problem *h.Error
	if !errors.As(err, &problem) || !problem.OutcomeUnknown || calls.Load() != 3 {
		t.Fatalf("bounded retry: calls=%d err=%v", calls.Load(), err)
	}
	calls.Store(0)
	_, _ = c.Release(
		context.Background(),
		h.Scope{TenantID: "tenant"},
		[]h.Reference{{ReferenceID: "r", FileID: "f"}},
	)
	if calls.Load() != 1 {
		t.Fatal("retried without caller deadline")
	}
}

func TestInternalStorageClientStrictReceipts(t *testing.T) {
	for _, body := range []string{`[]`, `null`, `[{"referenceID":"r","fileID":"f","state":"released","releaseRequiresDeletion":false}]`, `[{"referenceID":"r","fileID":"wrong","state":"released","releaseComplete":true,"releaseRequiresDeletion":false}]`, `[{"referenceID":"r","fileID":"f","state":"released","releaseComplete":false,"releaseRequiresDeletion":false}]`, `[{"referenceID":"r","fileID":"f","state":"released","releaseComplete":true,"releaseRequiresDeletion":false,"bucket":"leak"}]`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }),
			)
			defer srv.Close()
			c, _ := h.NewClient(h.ClientConfig{Endpoint: srv.URL, Token: "token"})
			defer c.Close()
			_, err := c.Release(
				context.Background(),
				h.Scope{TenantID: "tenant"},
				[]h.Reference{{ReferenceID: "r", FileID: "f"}},
			)
			var p *h.Error
			if !errors.As(err, &p) || !p.OutcomeUnknown {
				t.Fatalf("malformed receipt accepted: %v", err)
			}
		})
	}
}

// This controlled boundary proves auth and encoding; durable state behavior is tested with real databases.
type handoffBoundary struct {
	consumer string
	calls    int
}

func (b *handoffBoundary) Metadata(context.Context, string, h.Scope, []string) ([]h.FileMetadata, error) {
	panic("unused")
}

func (b *handoffBoundary) Bind(context.Context, string, h.Scope, []h.Reference) ([]h.BindResult, error) {
	panic("unused")
}

func (b *handoffBoundary) Statuses(context.Context, string, h.Scope, []h.Reference) ([]h.Status, error) {
	panic("unused")
}

func (b *handoffBoundary) Release(_ context.Context, c string, _ h.Scope, refs []h.Reference) ([]h.Status, error) {
	b.calls++
	b.consumer = c
	v := false
	return []h.Status{
		{
			ReferenceID:             refs[0].ReferenceID,
			FileID:                  refs[0].FileID,
			State:                   "released",
			ReleaseComplete:         true,
			ReleaseRequiresDeletion: &v,
		},
	}, nil
}

func TestInternalStorageAuthenticationAndScope(t *testing.T) {
	b := &handoffBoundary{}
	handler, err := h.NewHandler(
		b,
		[]h.Consumer{{ID: "stable", Tokens: []string{"old", "new"}, Scopes: []h.Scope{{TenantID: "tenant"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token, body string
		status      int
	}{{token: "old", body: `{"scope":{"tenantID":"tenant","projectID":null},"references":[{"referenceID":"r","fileID":"f"}]}`, status: 200}, {token: "new", body: `{"scope":{"tenantID":"tenant","projectID":null},"references":[{"referenceID":"r","fileID":"f"}]}`, status: 200}, {token: "wrong", body: `{}`, status: 401}, {token: "old", body: `{"scope":{"tenantID":"tenant","projectID":"project"},"references":[{"referenceID":"r","fileID":"f"}]}`, status: 403}, {token: "old", body: `{"scope":{"tenantID":"tenant"},"references":[{"referenceID":"r","fileID":"f"}]}`, status: 400}, {token: "old", body: `{"scope":{"tenantID":"tenant","projectID":null},"references":null}`, status: 400}, {token: "old", body: `{"scope":{"tenantID":"tenant","projectID":null},"references":[{"referenceID":"r","fileID":"f"},{"referenceID":"r","fileID":"g"}]}`, status: 400}, {token: "old", body: `{"scope":{"tenantID":"tenant","projectID":null},"consumerID":"spoof","references":[{"referenceID":"r","fileID":"f"}]}`, status: 400}} {
		req := httptest.NewRequest(
			"POST",
			"/internal/v1/storage/references/release",
			strings.NewReader(tc.body),
		)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	if b.calls != 2 || b.consumer != "stable" {
		t.Fatalf("consumer rotation: %+v", b)
	}
}

func TestInternalStorageRetryDeadlineAndDefiniteRejection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body, retry string
		calls       int
		unknown     bool
	}{
		{name: "definite", status: 409, body: `{"code":"ReferenceReleased"}`, retry: "", calls: 1, unknown: false},
		{name: "damaged rejection", status: 409, body: `invalid`, retry: "", calls: 3, unknown: true},
		{name: "retry after exceeds budget", status: 429, body: `{"code":"TooManyRequests"}`, retry: "10", calls: 1, unknown: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.retry)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c, _ := h.NewClient(h.ClientConfig{Endpoint: srv.URL, Token: "private"})
			defer c.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			_, err := c.Bind(
				ctx,
				h.Scope{TenantID: "tenant"},
				[]h.Reference{{ReferenceID: "r", FileID: "f"}},
			)
			var p *h.Error
			if !errors.As(err, &p) || p.OutcomeUnknown != tc.unknown || calls.Load() != int32(tc.calls) {
				t.Fatalf("calls=%d error=%+v", calls.Load(), p)
			}
		})
	}
}

func TestInternalStorageCancellationIsUnknown(t *testing.T) {
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := h.NewClient(h.ClientConfig{Endpoint: srv.URL, Token: "private"})
	defer c.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := c.Bind(ctx, h.Scope{TenantID: "tenant"}, []h.Reference{{ReferenceID: "r", FileID: "f"}})
		result <- err
	}()
	<-entered
	cancel()
	err := <-result
	var p *h.Error
	if !errors.As(err, &p) || !p.OutcomeUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost uncertainty/cause: %v", err)
	}
}

func TestInternalStorageMetadataNullableSize(t *testing.T) {
	for _, tc := range []struct {
		state  string
		accept bool
	}{{state: "uploading", accept: true}, {state: "deleting", accept: true}, {state: "deleted", accept: true}, {state: "ready", accept: false}, {state: "bound", accept: false}} {
		t.Run(tc.state, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(
					w,
					`[{"fileID":"f","state":%q,"size":null,"contentType":"application/octet-stream","declaredChecksum":null,"originalExpiresAt":"2026-10-15T00:00:00Z"}]`,
					tc.state,
				)
			}))
			defer srv.Close()
			c, err := h.NewClient(h.ClientConfig{Endpoint: srv.URL, Token: "token"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			files, err := c.Metadata(context.Background(), h.Scope{TenantID: "tenant"}, []string{"f"})
			if tc.accept {
				if err != nil || len(files) != 1 || files[0].Size != nil || files[0].State != tc.state {
					t.Fatalf("valid nullable metadata rejected: %+v %v", files, err)
				}
				return
			}
			var problem *h.Error
			if !errors.As(err, &problem) || !problem.OutcomeUnknown {
				t.Fatalf("missing confirmed size accepted: %+v %v", files, err)
			}
		})
	}
}
