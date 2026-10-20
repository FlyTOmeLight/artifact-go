package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	frameworkerrors "github.com/FlyTOmeLight/server-go/errors"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func clientFor(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *api.Client {
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

func TestCollectionOperations(t *testing.T) {
	var count int
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Content-Type", "application/json")
		switch count {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/artifacts" {
				t.Errorf("create: %s %s", r.Method, r.URL)
			}
			var body api.CreateArtifactRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Name != "model" || body.VersionScheme != api.VersionSchemeInteger {
				t.Errorf("body: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
		case 2:
			if r.Method != "GET" || r.URL.Path != "/v1/artifacts" ||
				r.URL.Query().Get("cursor") != "a+/=" ||
				r.URL.Query().Get("limit") != "2" ||
				r.URL.Query().Get("type") != "model" ||
				r.URL.Query().Get("state") != "active" {
				t.Errorf("list: %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"items":[{"id":"a01"}],"nextCursor":"next"}`)
			return
		case 3:
			if r.Method != "GET" || r.URL.Path != "/v1/artifacts/a01" {
				t.Errorf("get: %s %s", r.Method, r.URL)
			}
		case 4:
			if r.Method != "POST" || r.URL.Path != "/v1/artifacts/a01/archive" || r.ContentLength > 0 {
				t.Errorf("archive: %s %s length=%d", r.Method, r.URL, r.ContentLength)
			}
		}
		fmt.Fprint(
			w,
			`{"id":"a01","projectID":null,"retentionPolicy":{"enabled":false,"revision":0,"updatedAt":null}}`,
		)
	}, 0)
	ctx := context.Background()
	created, err := client.CreateArtifact(
		ctx,
		&api.CreateArtifactRequest{Type: "model", Name: "model", VersionScheme: api.VersionSchemeInteger},
	)
	if err != nil || created.ID != "a01" {
		t.Fatalf("create: %+v %v", created, err)
	}
	listed, err := client.ListArtifacts(
		ctx,
		&api.ListArtifactsRequest{
			PageQuery: api.PageQuery{Limit: 2, Cursor: "a+/="},
			Type:      "model",
			State:     api.ArtifactActive,
		},
	)
	if err != nil || len(listed.Items) != 1 || listed.NextCursor != "next" {
		t.Fatalf("list: %+v %v", listed, err)
	}
	if _, err = client.GetArtifact(
		ctx,
		&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: "a01"}},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ArchiveArtifact(
		ctx,
		&api.ArchiveArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: "a01"}},
	); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPFailuresPreserveStatusAndDoNotRetry(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType, code string
		status                        int
	}{
		{name: "json", body: `{"code":"ArtifactNameConflict","message":"exists","statusCode":409,"requestId":"req01"}`, contentType: "application/json", code: "ArtifactNameConflict", status: 409},
		{name: "proxy", body: "gateway unavailable", contentType: "text/plain", code: "HTTPError", status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}, 0)
			_, err := client.CreateArtifact(context.Background(), &api.CreateArtifactRequest{})
			var detail *frameworkerrors.Error
			if !errors.As(err, &detail) || detail.Status != tc.status || detail.Code != tc.code {
				t.Fatalf("error: %#v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("retried %d times", calls.Load())
			}
		})
	}
}

func TestRequestIdentityIsolation(t *testing.T) {
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		tenant := r.Header.Get("x-forge-tenant-id")
		if tenant == "" {
			if r.Header.Get("Authorization") != "" || r.Header.Get("x-forge-project-id") != "" {
				t.Error("identity leaked into anonymous request")
			}
		} else if r.Header.Get("Authorization") != "Bearer "+tenant || r.Header.Get("X-Request-ID") != tenant || r.Header.Get("x-forge-project-id") != tenant {
			t.Errorf("mixed identity for %s", tenant)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[]}`)
	}, 0)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			identity := fmt.Sprintf("tenant%d", i)
			project := identity
			ctx := api.WithRequestIdentity(
				context.Background(),
				api.RequestIdentity{
					BearerToken: identity,
					TenantID:    identity,
					ProjectID:   &project,
					RequestID:   identity,
				},
			)
			project = "changed-after-context"
			if _, err := client.ListArtifacts(ctx, &api.ListArtifactsRequest{}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if _, err := client.ListArtifacts(context.Background(), &api.ListArtifactsRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			started := make(chan struct{})
			client := clientFor(
				t,
				func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() },
				50*time.Millisecond,
			)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := client.GetArtifact(
					ctx,
					&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: "a01"}},
				)
				result <- err
			}()
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
