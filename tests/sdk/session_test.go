package sdk_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	artifact "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	storage "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

func TestSessionCredentialsArePerRequest(t *testing.T) {
	type credentials struct{ session, csrf, origin, bearer, requestID string }
	for _, domain := range []string{"artifact", "storage"} {
		t.Run(domain, func(t *testing.T) {
			handler := func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("write method changed: %s", r.Method)
				}
				id := r.Header.Get("X-Request-ID")
				wantSession, wantCSRF, wantOrigin, wantAuth := "", "", "", ""
				if strings.HasPrefix(id, "user-") {
					wantSession, wantCSRF, wantOrigin = "session-"+id, "csrf-"+id, "https://"+id+".forge.test"
				}
				if id == "service" {
					wantAuth = "Bearer management-token"
				}
				cookies := r.CookiesNamed("forge_session")
				if (wantSession == "" && len(cookies) != 0) ||
					(wantSession != "" && (len(cookies) != 1 || cookies[0].Value != wantSession)) {
					t.Errorf("request %q session cookie leaked or changed", id)
				}
				if r.Header.Get("X-CSRF-Token") != wantCSRF || r.Header.Get("Origin") != wantOrigin ||
					r.Header.Get("Authorization") != wantAuth {
					t.Errorf("request %q credentials leaked or changed", id)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if strings.Contains(string(body), "session-") ||
					strings.Contains(string(body), "csrf-") ||
					strings.Contains(string(body), ".forge.test") ||
					strings.Contains(string(body), "management-token") {
					t.Error("credentials copied into business JSON")
				}
				w.Header().Set("Content-Type", "application/json")
				http.SetCookie(
					w,
					&http.Cookie{Name: "forge_session", Value: "server-session", Path: "/"},
				)
				if r.URL.Path == "/v1/storage/files" {
					fmt.Fprint(w, `[]`)
				} else {
					fmt.Fprint(w, `{}`)
				}
			}
			var call func(context.Context, credentials) error
			if domain == "artifact" {
				client := clientFor(t, handler, 2*time.Second)
				call = func(ctx context.Context, v credentials) error {
					ctx = artifact.WithRequestIdentity(
						ctx,
						artifact.RequestIdentity{
							Session:     v.session,
							CSRFToken:   v.csrf,
							Origin:      v.origin,
							BearerToken: v.bearer,
							RequestID:   v.requestID,
						},
					)
					_, err := client.CreateArtifact(ctx, &artifact.CreateArtifactRequest{})
					return err
				}
			} else {
				client := storageClientFor(t, handler, 2*time.Second)
				call = func(ctx context.Context, v credentials) error {
					ctx = storage.WithRequestIdentity(
						ctx,
						storage.RequestIdentity{
							Session:     v.session,
							CSRFToken:   v.csrf,
							Origin:      v.origin,
							BearerToken: v.bearer,
							RequestID:   v.requestID,
						},
					)
					if v.bearer != "" {
						_, err := client.CreateTenantBucket(
							ctx,
							&storage.CreateTenantBucketRequest{TenantID: "tenant"},
						)
						return err
					}
					_, err := client.RegisterFiles(ctx, storage.RegisterFilesRequest{})
					return err
				}
			}
			// A response must not install a cookie on the shared client.
			if err := call(
				t.Context(),
				credentials{
					session:   "session-user-prime",
					csrf:      "csrf-user-prime",
					origin:    "https://user-prime.forge.test",
					requestID: "user-prime",
				},
			); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for i := 0; i < 24; i++ {
				wg.Go(func() {
					id := fmt.Sprintf("user-%d", i)
					if err := call(
						t.Context(),
						credentials{
							session:   "session-" + id,
							csrf:      "csrf-" + id,
							origin:    "https://" + id + ".forge.test",
							requestID: id,
						},
					); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			// Later calls on the same client must not inherit a user's credentials.
			for _, v := range []credentials{{bearer: "management-token", requestID: "service"}, {}} {
				if err := call(t.Context(), v); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSessionCredentialsDoNotFollowRedirects(t *testing.T) {
	for _, domain := range []string{"artifact", "storage"} {
		t.Run(domain, func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{}`)
			}))
			defer target.Close()
			handler := func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}
			var err error
			if domain == "artifact" {
				client := clientFor(t, handler, 2*time.Second)
				ctx := artifact.WithRequestIdentity(
					t.Context(),
					artifact.RequestIdentity{
						Session:   "session",
						CSRFToken: "csrf",
						Origin:    "https://forge.test",
					},
				)
				_, err = client.CreateArtifact(ctx, &artifact.CreateArtifactRequest{})
			} else {
				client := storageClientFor(t, handler, 2*time.Second)
				ctx := storage.WithRequestIdentity(
					t.Context(),
					storage.RequestIdentity{
						Session:   "session",
						CSRFToken: "csrf",
						Origin:    "https://forge.test",
					},
				)
				_, err = client.RegisterFiles(ctx, storage.RegisterFilesRequest{})
			}
			if received.Load() != 0 {
				t.Fatalf("redirect target received %d requests", received.Load())
			}
			if err == nil || !strings.Contains(err.Error(), "SDK refuses HTTP redirects") {
				t.Fatalf("expected explicit redirect refusal: %v", err)
			}
		})
	}
}
