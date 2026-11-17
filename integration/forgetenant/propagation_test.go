package forgetenant_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/constants"

	"github.com/FlyTOmeLight/artifact-go/integration/forgetenant"
	"github.com/FlyTOmeLight/artifact-go/service"
)

func propagationClient(t *testing.T, handler http.HandlerFunc, allowed []string) *forgetenant.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := forgetenant.New(forgetenant.Config{
		Endpoint: server.URL, ServiceToken: tenantToken, AllowedPropagationServices: allowed,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func propagationRequest(t *testing.T, project string) *http.Request {
	t.Helper()
	req := request(t, http.MethodGet, project)
	req.Header.Del("Cookie")
	req.Header.Set("Authorization", "Bearer caller-service-key")
	req.Header.Set(constants.HeaderImpersonateTenantID, "tenant")
	req.Header.Set(constants.HeaderImpersonateUserID, "user")
	if project != "" {
		req.Header.Set(constants.HeaderImpersonateProjectID, project)
	}
	return req
}

func TestResolveInternalPropagation(t *testing.T) {
	for _, project := range []string{"", "project"} {
		t.Run("project="+project, func(t *testing.T) {
			var calls atomic.Int32
			allowed := []string{"agentic-kit-api"}
			client := propagationClient(t, func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				if req.URL.Path != "/internal/v1/service/self" || req.Method != http.MethodGet ||
					req.Header.Get("Authorization") != "Bearer caller-service-key" {
					t.Error("caller service credential was not verified")
				}
				if req.Header.Get("Cookie") != "" ||
					req.Header.Get(constants.HeaderImpersonateUserID) != "" {
					t.Error("user proof sent to service authenticator")
				}
				fmt.Fprint(w, `{"service":"agentic-kit-api"}`)
			}, allowed)
			allowed[0] = "mutated"
			req := propagationRequest(t, project)
			req.Header.Set("X-Resource-User-ID", "user")
			identity, err := client.Resolve(req)
			if err != nil {
				t.Fatal(err)
			}
			if identity.UserID != "user" || identity.TenantID != "tenant" ||
				identity.OwnershipState != service.OwnershipActive || identity.ResourceUserID == nil ||
				*identity.ResourceUserID != "user" || (identity.ProjectID == nil) != (project == "") ||
				(identity.ProjectID != nil && *identity.ProjectID != project) {
				t.Fatalf("incorrect propagated identity: %+v", identity)
			}
			if calls.Load() != 1 {
				t.Fatalf("expected one service check, got %d", calls.Load())
			}
		})
	}
}

func TestResolvePropagationRejectsInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*http.Request)
		status int
		code   string
	}{
		{name: "missing bearer", change: func(req *http.Request) { req.Header.Del("Authorization") }, status: 401, code: "SERVICE_AUTH_REQUIRED"},
		{name: "empty bearer", change: func(req *http.Request) { req.Header.Set("Authorization", "Bearer") }, status: 401, code: "SERVICE_AUTH_REQUIRED"},
		{name: "spaced key", change: func(req *http.Request) { req.Header.Set("Authorization", "Bearer invalid key") }, status: 401, code: "SERVICE_AUTH_REQUIRED"},
		{name: "cookie", change: func(req *http.Request) { req.AddCookie(&http.Cookie{Name: "forge_session", Value: "user-session"}) }, status: 400, code: "AMBIGUOUS_IDENTITY_PROOF"},
		{name: "missing user", change: func(req *http.Request) { req.Header.Del(constants.HeaderImpersonateUserID) }, status: 400, code: "INVALID_SCOPE"},
		{name: "missing tenant", change: func(req *http.Request) { req.Header.Del(constants.HeaderImpersonateTenantID) }, status: 400, code: "INVALID_SCOPE"},
		{name: "empty user", change: func(req *http.Request) { req.Header.Set(constants.HeaderImpersonateUserID, "") }, status: 400, code: "INVALID_SCOPE"},
		{name: "duplicate user", change: func(req *http.Request) { req.Header.Add(constants.HeaderImpersonateUserID, "other") }, status: 400, code: "INVALID_SCOPE"},
		{name: "padded user", change: func(req *http.Request) { req.Header.Set(constants.HeaderImpersonateUserID, " user") }, status: 400, code: "INVALID_SCOPE"},
		{name: "other tenant", change: func(req *http.Request) { req.Header.Set(constants.HeaderImpersonateTenantID, "other") }, status: 403, code: "ScopeAccessDenied"},
		{name: "other project", change: func(req *http.Request) { req.Header.Set(constants.HeaderImpersonateProjectID, "other") }, status: 403, code: "ScopeAccessDenied"},
		{name: "missing project", change: func(req *http.Request) { req.Header.Del(constants.HeaderImpersonateProjectID) }, status: 403, code: "ScopeAccessDenied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := propagationClient(
				t,
				func(http.ResponseWriter, *http.Request) { calls.Add(1) },
				[]string{"agentic-kit-api"},
			)
			req := propagationRequest(t, "project")
			test.change(req)
			_, err := client.Resolve(req)
			expectError(t, err, test.status, test.code)
			if calls.Load() != 0 {
				t.Fatal("invalid propagation reached Tenant")
			}
		})
	}
}

func TestResolveRejectsMixedProofWithDuplicateSession(t *testing.T) {
	for _, mode := range []string{"api key", "propagation"} {
		for _, splitHeaders := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/split=%t", mode, splitHeaders), func(t *testing.T) {
				var calls atomic.Int32
				client := propagationClient(t, func(http.ResponseWriter, *http.Request) {
					calls.Add(1)
				}, []string{"agentic-kit-api"})
				req := propagationRequest(t, "")
				if mode == "api key" {
					req.Header.Del(constants.HeaderImpersonateTenantID)
					req.Header.Del(constants.HeaderImpersonateUserID)
				}
				if splitHeaders {
					req.Header.Add("Cookie", "forge_session=")
					req.Header.Add("Cookie", "forge_session=existing-session")
				} else {
					req.Header.Set("Cookie", "forge_session=; forge_session=existing-session")
				}
				_, err := client.Resolve(req)
				expectError(t, err, 400, "AMBIGUOUS_IDENTITY_PROOF")
				if calls.Load() != 0 {
					t.Fatal("mixed credentials reached Tenant")
				}
			})
		}
	}
}

func TestResolvePropagationDisabledDoesNotFallBack(t *testing.T) {
	var calls atomic.Int32
	client := propagationClient(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) }, nil)
	for _, header := range []string{constants.HeaderImpersonateTenantID, constants.HeaderImpersonateUserID, constants.HeaderImpersonateProjectID} {
		req := request(t, http.MethodGet, "")
		req.Header.Set(header, "")
		_, err := client.Resolve(req)
		expectError(t, err, 403, "ScopeAccessDenied")
	}
	if calls.Load() != 0 {
		t.Fatal("disabled propagation fell back to user authentication")
	}
}

func TestResolvePropagationServiceFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "rejected credential", status: 401, body: `{"error":{"code":"SERVICE_AUTH_REQUIRED"}}`, wantStatus: 401, wantCode: "SERVICE_AUTH_REQUIRED"},
		{name: "user key", status: 401, body: `{"error":{"code":"SERVICE_AUTH_REQUIRED"}}`, wantStatus: 401, wantCode: "SERVICE_AUTH_REQUIRED"},
		{name: "denied service", status: 200, body: `{"service":"other"}`, wantStatus: 403, wantCode: "ScopeAccessDenied"},
		{name: "outage", status: 503, body: `upstream-sensitive-detail`, wantStatus: 503, wantCode: "TenantIdentityUnavailable"},
		{name: "unexpected forbidden", status: 403, body: `{}`, wantStatus: 503, wantCode: "TenantIdentityUnavailable"},
		{name: "invalid JSON", status: 200, body: `{`, wantStatus: 503, wantCode: "TenantIdentityUnavailable"},
		{name: "null", status: 200, body: `null`, wantStatus: 503, wantCode: "TenantIdentityUnavailable"},
		{name: "empty name", status: 200, body: `{"service":""}`, wantStatus: 503, wantCode: "TenantIdentityUnavailable"},
		{name: "trailing JSON", status: 200, body: `{"service":"agentic-kit-api"} {}`, wantStatus: 503, wantCode: "TenantIdentityUnavailable"},
		{name: "oversized", status: 200, body: strings.Repeat(" ", (64<<10)+1), wantStatus: 503, wantCode: "TenantIdentityUnavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := propagationClient(t, func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}, []string{"agentic-kit-api"})
			_, err := client.Resolve(propagationRequest(t, ""))
			expectError(t, err, test.wantStatus, test.wantCode)
		})
	}
}

func TestResolvePropagationPreservesPrivateOwnership(t *testing.T) {
	client := propagationClient(t, func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"service":"agentic-kit-api"}`)
	}, []string{"agentic-kit-api"})
	req := propagationRequest(t, "")
	req.Header.Set("X-Resource-User-ID", "other")
	_, err := client.Resolve(req)
	expectError(t, err, 403, "ScopeAccessDenied")
}

func TestResolvePropagationCancellation(t *testing.T) {
	client := propagationClient(t, func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	}, []string{"agentic-kit-api"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.Resolve(propagationRequest(t, "").WithContext(ctx))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
