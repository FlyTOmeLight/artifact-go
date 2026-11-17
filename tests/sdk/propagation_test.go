package sdk_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/constants"
	frameworkerrors "github.com/FlyTOmeLight/server-go/errors"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func TestPropagationIdentityHeaders(t *testing.T) {
	project := "project-01"
	emptyProject := ""
	resourceUser := "owner-01"

	type observedRequest struct {
		auth               string
		cookies            []string
		csrf               string
		origin             string
		tenant             []string
		project            []string
		impersonateTenant  []string
		impersonateUser    []string
		impersonateProject []string
		resourceUser       []string
		body               string
	}

	cases := []struct {
		name              string
		identity          api.RequestIdentity
		status            int
		responseBody      string
		wantAuth          string
		wantCookies       []string
		wantCSRF          string
		wantOrigin        string
		wantTenant        []string
		wantProject       []string
		wantImpTenant     []string
		wantImpUser       []string
		wantImpProject    []string
		wantResourceUser  []string
		wantErrorCode     string
		wantErrorHTTPCode int
	}{
		{
			name: "project_scope_propagation",
			identity: api.RequestIdentity{
				BearerToken:      "service-secret-token",
				TenantID:         "tenant-01",
				ProjectID:        &project,
				PropagatedUserID: "user-01",
				ResourceUserID:   &resourceUser,
				RequestID:        "req-project",
			},
			status:           http.StatusCreated,
			responseBody:     `{"id":"a01"}`,
			wantAuth:         "Bearer service-secret-token",
			wantTenant:       []string{"tenant-01"},
			wantProject:      []string{"project-01"},
			wantImpTenant:    []string{"tenant-01"},
			wantImpUser:      []string{"user-01"},
			wantImpProject:   []string{"project-01"},
			wantResourceUser: []string{"owner-01"},
		},
		{
			name: "tenant_scope_propagation_omits_project_headers",
			identity: api.RequestIdentity{
				BearerToken:      "service-secret-token",
				TenantID:         "tenant-02",
				PropagatedUserID: "user-02",
				RequestID:        "req-tenant",
			},
			status:        http.StatusCreated,
			responseBody:  `{"id":"a02"}`,
			wantAuth:      "Bearer service-secret-token",
			wantTenant:    []string{"tenant-02"},
			wantImpTenant: []string{"tenant-02"},
			wantImpUser:   []string{"user-02"},
		},
		{
			name: "explicit_empty_project_is_preserved",
			identity: api.RequestIdentity{
				BearerToken:      "service-secret-token",
				TenantID:         "tenant-02",
				ProjectID:        &emptyProject,
				PropagatedUserID: "user-02",
			},
			status:         http.StatusCreated,
			responseBody:   `{"id":"a02"}`,
			wantAuth:       "Bearer service-secret-token",
			wantTenant:     []string{"tenant-02"},
			wantImpTenant:  []string{"tenant-02"},
			wantImpUser:    []string{"user-02"},
			wantImpProject: []string{""},
		},
		{
			name: "browser_user_omits_propagation_headers",
			identity: api.RequestIdentity{
				Session:   "session-user-01",
				CSRFToken: "csrf-01",
				Origin:    "https://forge.test",
				TenantID:  "tenant-01",
				ProjectID: &project,
				RequestID: "req-session",
			},
			status:       http.StatusCreated,
			responseBody: `{"id":"a03"}`,
			wantCookies:  []string{"session-user-01"},
			wantCSRF:     "csrf-01",
			wantOrigin:   "https://forge.test",
			wantTenant:   []string{"tenant-01"},
			wantProject:  []string{"project-01"},
		},
		{
			name: "api_key_user_omits_propagation_headers",
			identity: api.RequestIdentity{
				BearerToken: "user-api-key",
				TenantID:    "tenant-01",
				ProjectID:   &project,
				RequestID:   "req-apikey",
			},
			status:       http.StatusCreated,
			responseBody: `{"id":"a04"}`,
			wantAuth:     "Bearer user-api-key",
			wantTenant:   []string{"tenant-01"},
			wantProject:  []string{"project-01"},
		},
		{
			name: "mixed_credentials_preserve_cookie_for_server_rejection",
			identity: api.RequestIdentity{
				BearerToken:      "service-secret-token",
				Session:          "mixed-session",
				CSRFToken:        "mixed-csrf",
				Origin:           "https://forge.test",
				TenantID:         "tenant-01",
				PropagatedUserID: "user-01",
				RequestID:        "req-mixed",
			},
			status:            http.StatusBadRequest,
			responseBody:      `{"code":"AMBIGUOUS_IDENTITY_PROOF","message":"internal propagation cannot carry a session","statusCode":400}`,
			wantAuth:          "Bearer service-secret-token",
			wantCookies:       []string{"mixed-session"},
			wantCSRF:          "mixed-csrf",
			wantOrigin:        "https://forge.test",
			wantTenant:        []string{"tenant-01"},
			wantImpTenant:     []string{"tenant-01"},
			wantImpUser:       []string{"user-01"},
			wantErrorCode:     "AMBIGUOUS_IDENTITY_PROOF",
			wantErrorHTTPCode: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got observedRequest
			client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				var cookies []string
				for _, cookie := range r.CookiesNamed("forge_session") {
					cookies = append(cookies, cookie.Value)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				got = observedRequest{
					auth:               r.Header.Get(constants.HeaderAuthorization),
					cookies:            cookies,
					csrf:               r.Header.Get("X-CSRF-Token"),
					origin:             r.Header.Get("Origin"),
					tenant:             r.Header.Values(constants.HeaderTenantID),
					project:            r.Header.Values(constants.HeaderProjectID),
					impersonateTenant:  r.Header.Values(constants.HeaderImpersonateTenantID),
					impersonateUser:    r.Header.Values(constants.HeaderImpersonateUserID),
					impersonateProject: r.Header.Values(constants.HeaderImpersonateProjectID),
					resourceUser:       r.Header.Values("X-Resource-User-ID"),
					body:               string(raw),
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.responseBody)
			}, 2*time.Second)

			ctx := api.WithRequestIdentity(t.Context(), tc.identity)
			_, err := client.CreateArtifact(ctx, &api.CreateArtifactRequest{
				Type:          "agent",
				Name:          "demo",
				VersionScheme: api.VersionSchemeInteger,
			})
			if tc.wantErrorCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				var apiErr *frameworkerrors.Error
				if !errors.As(err, &apiErr) || apiErr.Status != tc.wantErrorHTTPCode ||
					apiErr.Code != tc.wantErrorCode {
					t.Fatalf(
						"error = %v, want HTTP %d %s",
						err,
						tc.wantErrorHTTPCode,
						tc.wantErrorCode,
					)
				}
			}

			if got.auth != tc.wantAuth {
				t.Fatalf("authorization = %q, want %q", got.auth, tc.wantAuth)
			}
			if !reflect.DeepEqual(got.cookies, tc.wantCookies) {
				t.Fatalf("forge_session cookies = %v, want %v", got.cookies, tc.wantCookies)
			}
			if got.csrf != tc.wantCSRF || got.origin != tc.wantOrigin {
				t.Fatalf(
					"csrf/origin = %q/%q, want %q/%q",
					got.csrf,
					got.origin,
					tc.wantCSRF,
					tc.wantOrigin,
				)
			}
			if !reflect.DeepEqual(got.tenant, tc.wantTenant) {
				t.Fatalf("x-forge-tenant-id = %v, want %v", got.tenant, tc.wantTenant)
			}
			if !reflect.DeepEqual(got.project, tc.wantProject) {
				t.Fatalf("x-forge-project-id = %v, want %v", got.project, tc.wantProject)
			}
			if !reflect.DeepEqual(got.impersonateTenant, tc.wantImpTenant) {
				t.Fatalf(
					"x-impersonate-target-tenant-id = %v, want %v",
					got.impersonateTenant,
					tc.wantImpTenant,
				)
			}
			if !reflect.DeepEqual(got.impersonateUser, tc.wantImpUser) {
				t.Fatalf(
					"x-impersonate-target-user-id = %v, want %v",
					got.impersonateUser,
					tc.wantImpUser,
				)
			}
			if !reflect.DeepEqual(got.impersonateProject, tc.wantImpProject) {
				t.Fatalf(
					"x-impersonate-target-project-id = %v, want %v",
					got.impersonateProject,
					tc.wantImpProject,
				)
			}
			if !reflect.DeepEqual(got.resourceUser, tc.wantResourceUser) {
				t.Fatalf("X-Resource-User-ID = %v, want %v", got.resourceUser, tc.wantResourceUser)
			}
			for _, secret := range []string{"service-secret-token", "user-01", "user-02", "mixed-session"} {
				if strings.Contains(got.body, secret) {
					t.Fatalf("identity value %q leaked into JSON body %s", secret, got.body)
				}
			}
		})
	}
}

func TestPropagationIdentityIsIsolatedPerRequest(t *testing.T) {
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		switch {
		case strings.HasPrefix(id, "prop-project-"):
			suffix := strings.TrimPrefix(id, "prop-project-")
			if r.Header.Get(constants.HeaderAuthorization) != "Bearer svc-"+suffix ||
				len(r.CookiesNamed("forge_session")) != 0 ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderTenantID),
					[]string{"tenant-" + suffix},
				) ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderProjectID),
					[]string{"project-" + suffix},
				) ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderImpersonateTenantID),
					[]string{"tenant-" + suffix},
				) ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderImpersonateUserID),
					[]string{"user-" + suffix},
				) ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderImpersonateProjectID),
					[]string{"project-" + suffix},
				) {
				t.Errorf("project propagation headers leaked or mismatched for %s: %v", id, r.Header)
			}
		case strings.HasPrefix(id, "prop-tenant-"):
			suffix := strings.TrimPrefix(id, "prop-tenant-")
			if r.Header.Get(constants.HeaderAuthorization) != "Bearer svc-"+suffix ||
				len(r.CookiesNamed("forge_session")) != 0 ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderTenantID),
					[]string{"tenant-" + suffix},
				) ||
				len(r.Header.Values(constants.HeaderProjectID)) != 0 ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderImpersonateTenantID),
					[]string{"tenant-" + suffix},
				) ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderImpersonateUserID),
					[]string{"user-" + suffix},
				) ||
				len(r.Header.Values(constants.HeaderImpersonateProjectID)) != 0 {
				t.Errorf("tenant propagation headers leaked or mismatched for %s: %v", id, r.Header)
			}
		case strings.HasPrefix(id, "session-"):
			suffix := strings.TrimPrefix(id, "session-")
			cookies := r.CookiesNamed("forge_session")
			if r.Header.Get(constants.HeaderAuthorization) != "" ||
				len(cookies) != 1 || cookies[0].Value != "sess-"+suffix ||
				!reflect.DeepEqual(
					r.Header.Values(constants.HeaderTenantID),
					[]string{"tenant-" + suffix},
				) ||
				len(r.Header.Values(constants.HeaderImpersonateTenantID)) != 0 ||
				len(r.Header.Values(constants.HeaderImpersonateUserID)) != 0 ||
				len(r.Header.Values(constants.HeaderImpersonateProjectID)) != 0 {
				t.Errorf("session headers leaked or mismatched for %s: %v", id, r.Header)
			}
		case id == "":
			if r.Header.Get(constants.HeaderAuthorization) != "" ||
				len(r.CookiesNamed("forge_session")) != 0 ||
				len(r.Header.Values(constants.HeaderTenantID)) != 0 ||
				len(r.Header.Values(constants.HeaderProjectID)) != 0 ||
				len(r.Header.Values(constants.HeaderImpersonateTenantID)) != 0 ||
				len(r.Header.Values(constants.HeaderImpersonateUserID)) != 0 ||
				len(r.Header.Values(constants.HeaderImpersonateProjectID)) != 0 {
				t.Errorf("unauthenticated call inherited headers: %v", r.Header)
			}
		default:
			t.Errorf("unexpected request ID %q", id)
		}
		http.SetCookie(w, &http.Cookie{Name: "forge_session", Value: "server-cookie", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[]}`)
	}, 2*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		suffix := strconv.Itoa(i)
		wg.Go(func() {
			project := "project-" + suffix
			ctx := api.WithRequestIdentity(t.Context(), api.RequestIdentity{
				BearerToken:      "svc-" + suffix,
				TenantID:         "tenant-" + suffix,
				ProjectID:        &project,
				PropagatedUserID: "user-" + suffix,
				RequestID:        "prop-project-" + suffix,
			})
			if _, err := client.ListArtifacts(ctx, &api.ListArtifactsRequest{}); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			ctx := api.WithRequestIdentity(t.Context(), api.RequestIdentity{
				BearerToken:      "svc-" + suffix,
				TenantID:         "tenant-" + suffix,
				PropagatedUserID: "user-" + suffix,
				RequestID:        "prop-tenant-" + suffix,
			})
			if _, err := client.ListArtifacts(ctx, &api.ListArtifactsRequest{}); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			ctx := api.WithRequestIdentity(t.Context(), api.RequestIdentity{
				Session:   "sess-" + suffix,
				TenantID:  "tenant-" + suffix,
				RequestID: "session-" + suffix,
			})
			if _, err := client.ListArtifacts(ctx, &api.ListArtifactsRequest{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if _, err := client.ListArtifacts(context.Background(), &api.ListArtifactsRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestPropagationContextSnapshot(t *testing.T) {
	type snapshot struct {
		auth               string
		tenant             string
		project            string
		impersonateTenant  string
		impersonateUser    string
		impersonateProject string
		resourceUser       string
	}
	seen := make(chan snapshot, 2)
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- snapshot{
			auth:               r.Header.Get(constants.HeaderAuthorization),
			tenant:             r.Header.Get(constants.HeaderTenantID),
			project:            r.Header.Get(constants.HeaderProjectID),
			impersonateTenant:  r.Header.Get(constants.HeaderImpersonateTenantID),
			impersonateUser:    r.Header.Get(constants.HeaderImpersonateUserID),
			impersonateProject: r.Header.Get(constants.HeaderImpersonateProjectID),
			resourceUser:       r.Header.Get("X-Resource-User-ID"),
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[]}`)
	}, 2*time.Second)

	project := "project-original"
	resourceUser := "owner-original"
	identity := api.RequestIdentity{
		BearerToken:      "svc-original",
		TenantID:         "tenant-original",
		ProjectID:        &project,
		PropagatedUserID: "user-original",
		ResourceUserID:   &resourceUser,
	}
	ctx := api.WithRequestIdentity(t.Context(), identity)

	project = "project-mutated"
	resourceUser = "owner-mutated"
	identity.BearerToken = "svc-mutated"
	identity.TenantID = "tenant-mutated"
	identity.PropagatedUserID = "user-mutated"

	if _, err := client.ListArtifacts(ctx, &api.ListArtifactsRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListArtifacts(t.Context(), &api.ListArtifactsRequest{}); err != nil {
		t.Fatal(err)
	}

	wantFirst := snapshot{
		auth:               "Bearer svc-original",
		tenant:             "tenant-original",
		project:            "project-original",
		impersonateTenant:  "tenant-original",
		impersonateUser:    "user-original",
		impersonateProject: "project-original",
		resourceUser:       "owner-original",
	}
	if got := <-seen; got != wantFirst {
		t.Fatalf("first call snapshot = %+v, want %+v", got, wantFirst)
	}
	if got := <-seen; got != (snapshot{}) {
		t.Fatalf("base context call = %+v, want empty snapshot", got)
	}
}
