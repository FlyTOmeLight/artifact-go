package forgetenant_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/constants"
	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/artifact-go/integration/forgetenant"
	"github.com/FlyTOmeLight/artifact-go/service"
)

var tenantToken = strings.Repeat("t", 32)

func newClient(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *forgetenant.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := forgetenant.New(
		forgetenant.Config{Endpoint: server.URL, ServiceToken: tenantToken, Timeout: timeout},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func request(t *testing.T, method, project string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "http://artifact.test/v1/artifacts", nil).WithContext(t.Context())
	r.AddCookie(&http.Cookie{Name: "forge_session", Value: "user-session"})
	r.Header.Set(constants.HeaderTenantID, "tenant")
	if project != "" {
		r.Header.Set(constants.HeaderProjectID, project)
	}
	r.Header.Set("Origin", "https://forge.test")
	r.Header.Set("X-CSRF-Token", "csrf-value")
	return r
}

func expectError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var detail *serrors.Error
	if !errors.As(err, &detail) || detail.Status != status || detail.Code != code {
		t.Fatalf("expected %d/%s; got %v", status, code, err)
	}
	for _, secret := range []string{"user-session", "csrf-value", tenantToken, "upstream-sensitive-detail"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error exposes credentials or upstream detail")
		}
	}
}

func TestResolveWireAndExactActiveScope(t *testing.T) {
	for _, project := range []string{"", "project"} {
		t.Run("project="+project, func(t *testing.T) {
			var calls atomic.Int32
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/scope/resolve" ||
					r.Header.Get("Authorization") != "Bearer "+tenantToken {
					t.Error("invalid internal scope request")
				}
				if len(r.Cookies()) != 0 || r.Header.Get("Origin") != "" ||
					r.Header.Get("X-CSRF-Token") != "" {
					t.Error("user credentials copied outside scope-check JSON")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				want := map[string]string{
					"session":   "user-session",
					"method":    "POST",
					"origin":    "https://forge.test",
					"csrf":      "csrf-value",
					"tenant_id": "tenant",
				}
				if len(body) != 6 {
					t.Errorf("unexpected body fields: %d", len(body))
				}
				for key, value := range want {
					var got string
					if json.Unmarshal(body[key], &got) != nil || got != value {
						t.Errorf("field %s changed", key)
					}
				}
				var gotProject *string
				if len(body["project_id"]) == 0 ||
					json.Unmarshal(body["project_id"], &gotProject) != nil ||
					(project == "") != (gotProject == nil) ||
					(gotProject != nil && *gotProject != project) {
					t.Error("scope selection changed")
				}
				http.SetCookie(
					w,
					&http.Cookie{Name: "forge_session", Value: "server-session", Path: "/"},
				)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).
					Encode(map[string]any{"principal": map[string]any{"id": "user", "operator": true}, "user_id": "user", "tenant_id": "tenant", "project_id": gotProject, "ownership_state": "active"})
			}, 0)
			var resolve service.IdentityResolver = client.Resolve
			for range 2 {
				identity, err := resolve(request(t, http.MethodPost, project))
				if err != nil || identity.UserID != "user" || identity.TenantID != "tenant" ||
					identity.OwnershipState != service.OwnershipActive ||
					(project == "") != (identity.ProjectID == nil) ||
					(identity.ProjectID != nil && *identity.ProjectID != project) {
					t.Fatalf("unexpected identity: %+v %v", identity, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("unexpected calls %d", calls.Load())
			}
		})
	}
}

func TestResolveRejectsUnverifiedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		status, wantStatus int
		code               string
	}{
		{name: "expired", body: `{"error":{"code":"AUTH_REQUIRED","message":"upstream-sensitive-detail"}}`, status: 401, wantStatus: 401, code: "AUTH_REQUIRED"},
		{name: "csrf", body: `{"error":{"code":"CSRF_REJECTED"}}`, status: 403, wantStatus: 403, code: "CSRF_REJECTED"},
		{name: "member", body: `{"error":{"code":"SCOPE_ACCESS_DENIED"}}`, status: 403, wantStatus: 403, code: "SCOPE_ACCESS_DENIED"},
		{name: "inactive", body: `{"error":{"code":"SCOPE_INACTIVE"}}`, status: 403, wantStatus: 403, code: "SCOPE_INACTIVE"},
		{name: "missing", body: `{"error":{"code":"SCOPE_NOT_FOUND"}}`, status: 404, wantStatus: 404, code: "SCOPE_NOT_FOUND"},
		{name: "service-token", body: `{"error":{"code":"SERVICE_AUTH_REQUIRED"}}`, status: 401, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "wrong-code-status", body: `{"error":{"code":"AUTH_REQUIRED"}}`, status: 500, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "unknown-error", body: `{"error":{"code":"UNKNOWN","message":"upstream-sensitive-detail"}}`, status: 403, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "failure", body: `upstream-sensitive-detail`, status: 503, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "malformed", body: `{"user_id":`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "missing principal", body: `{"user_id":"user","tenant_id":"tenant","project_id":null,"ownership_state":"active"}`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "mismatched principal", body: `{"principal":{"id":"other"},"user_id":"user","tenant_id":"tenant","project_id":null,"ownership_state":"active"}`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "deleted", body: `{"principal":{"id":"user"},"user_id":"user","tenant_id":"tenant","project_id":null,"ownership_state":"deleted"}`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "tenant-mismatch", body: `{"principal":{"id":"user"},"user_id":"user","tenant_id":"foreign","project_id":null,"ownership_state":"active"}`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "invalid scope", body: `{"error":{"code":"INVALID_SCOPE"}}`, status: 422, wantStatus: 422, code: "INVALID_SCOPE"},
		{name: "scope required", body: `{"error":{"code":"SCOPE_REQUIRED"}}`, status: 400, wantStatus: 400, code: "SCOPE_REQUIRED"},
		{name: "project-mismatch", body: `{"principal":{"id":"user"},"user_id":"user","tenant_id":"tenant","project_id":"foreign","ownership_state":"active"}`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "missing-user", body: `{"tenant_id":"tenant","project_id":null,"ownership_state":"active"}`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "missing-project-field", body: `{"user_id":"user","tenant_id":"tenant","ownership_state":"active"}`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "trailing-json", body: `{"principal":{"id":"user","operator":true},"user_id":"user","tenant_id":"tenant","project_id":null,"ownership_state":"active"} {}`, status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
		{name: "oversized", body: strings.Repeat("x", (64<<10)+1), status: 200, wantStatus: 503, code: "TenantIdentityUnavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}, 0)
			identity, err := client.Resolve(request(t, http.MethodGet, ""))
			expectError(t, err, tc.wantStatus, tc.code)
			if identity.UserID != "" || identity.OwnershipState != "" {
				t.Fatal("failed response produced identity")
			}
			if calls.Load() != 1 {
				t.Fatalf("retried %d times", calls.Load())
			}
		})
	}
}

func TestResolveMissingCredentialsAndSelection(t *testing.T) {
	var calls atomic.Int32
	client := newClient(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) }, 0)
	r := request(t, http.MethodPost, "")
	r.Header.Del("Cookie")
	_, err := client.Resolve(r)
	expectError(t, err, 401, "AUTH_REQUIRED")
	r = request(t, http.MethodGet, "")
	r.Header.Del(constants.HeaderTenantID)
	_, err = client.Resolve(r)
	expectError(t, err, 400, "INVALID_SCOPE")
	r = request(t, http.MethodGet, "")
	r.Header.Add(constants.HeaderTenantID, "other")
	_, err = client.Resolve(r)
	expectError(t, err, 400, "INVALID_SCOPE")
	r = request(t, http.MethodGet, "")
	r.Header.Set("X-Tenant-ID", "tenant")
	r.Header.Del(constants.HeaderTenantID)
	_, err = client.Resolve(r)
	expectError(t, err, 400, "INVALID_SCOPE")
	if calls.Load() != 0 {
		t.Fatal("invalid local request reached Tenant")
	}
}

func TestResolvePreservesGetContextAndNetworkFailures(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Method, Origin, CSRF string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Method != "GET" || body.Origin != "" || body.CSRF != "" {
			t.Error("GET metadata was synthesized")
		}
		fmt.Fprint(
			w,
			`{"principal":{"id":"user","operator":true},"user_id":"user","tenant_id":"tenant","project_id":null,"ownership_state":"active"}`,
		)
	}, 0)
	r := request(t, http.MethodGet, "")
	r.Header.Del("Origin")
	r.Header.Del("X-CSRF-Token")
	if _, err := client.Resolve(r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.Resolve(r.WithContext(ctx))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation lost: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	closed, err := forgetenant.New(forgetenant.Config{Endpoint: server.URL, ServiceToken: tenantToken})
	if err != nil {
		t.Fatal(err)
	}
	defer closed.Close()
	_, err = closed.Resolve(request(t, http.MethodGet, ""))
	expectError(t, err, 503, "TenantIdentityUnavailable")
}

func TestResolveTimeoutAndRedirect(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}, 20*time.Millisecond)
		defer close(release)
		_, err := client.Resolve(request(t, http.MethodGet, ""))
		expectError(t, err, 503, "TenantIdentityUnavailable")
	})
	t.Run("redirect", func(t *testing.T) {
		var received atomic.Int32
		target := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }),
		)
		defer target.Close()
		client := newClient(
			t,
			func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) },
			0,
		)
		_, err := client.Resolve(request(t, http.MethodPost, ""))
		expectError(t, err, 503, "TenantIdentityUnavailable")
		if received.Load() != 0 {
			t.Fatal("credentials followed redirect")
		}
	})
}

func TestConfigurationAndManagementTokens(t *testing.T) {
	for _, endpoint := range []string{"relative", "http://tenant.example", "https://user:secret@tenant.example", "https://tenant.example/internal", "https://tenant.example?", "https://tenant.example#fragment"} {
		if _, err := forgetenant.New(
			forgetenant.Config{Endpoint: endpoint, ServiceToken: tenantToken},
		); err == nil {
			t.Fatalf("accepted endpoint %s", endpoint)
		}
	}
	for _, cfg := range []forgetenant.Config{{Endpoint: "https://tenant.example", ServiceToken: "short"}, {Endpoint: "https://tenant.example", ServiceToken: tenantToken + "\n"}, {Endpoint: "https://tenant.example", ServiceToken: tenantToken, Timeout: -time.Second}} {
		if _, err := forgetenant.New(cfg); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	old, newToken := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, tokens := range [][]string{nil, {"short"}, {old, old}, {old + "\n"}} {
		if _, err := forgetenant.NewManagementVerifier(tokens); err == nil {
			t.Fatal("invalid management tokens accepted")
		}
	}
	tokens := []string{old, newToken}
	verify, err := forgetenant.NewManagementVerifier(tokens)
	if err != nil {
		t.Fatal(err)
	}
	tokens[0] = "mutated"
	for _, token := range []string{old, newToken} {
		r := request(t, http.MethodPost, "")
		r.Header.Set("Authorization", "Bearer "+token)
		if err := verify(r); err != nil {
			t.Fatal(err)
		}
	}
	for _, auth := range []string{"", old, "Bearer wrong", "Bearer " + tenantToken} {
		r := request(t, http.MethodPost, "")
		r.Header.Set("Authorization", auth)
		expectError(t, verify(r), 401, "TenantServiceUnauthorized")
	}
	rotated, err := forgetenant.NewManagementVerifier([]string{newToken})
	if err != nil {
		t.Fatal(err)
	}
	r := request(t, http.MethodPost, "")
	r.Header.Set("Authorization", "Bearer "+old)
	expectError(t, rotated(r), 401, "TenantServiceUnauthorized")
}
