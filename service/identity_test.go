package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	serrors "github.com/FlyTOmeLight/server-go/errors"
)

func TestMalformedResourceUserIsInvalidScopeBeforeResolverDenial(t *testing.T) {
	var calls int
	resolve := func(r *http.Request) (RequestIdentity, error) {
		calls++
		identity := RequestIdentity{UserID: "user", TenantID: "tenant", OwnershipState: OwnershipActive}
		value := r.Header.Get("X-Resource-User-ID")
		if value == "" {
			return identity, nil
		}
		if value != identity.UserID {
			return RequestIdentity{}, serrors.NewError(
				403,
				"ScopeAccessDenied",
				"private resources belong to another user",
			)
		}
		selected := value
		identity.ResourceUserID = &selected
		return identity, nil
	}
	for _, value := range []string{"", " ", "\t", "\n", " user", "user ", "user\t", "\u00a0", "\u00a0user"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			calls = 0
			r := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
			r.Header.Set("X-Resource-User-ID", value)
			_, err := resolveResourceIdentity(resolve, r)
			expectIdentityError(t, err, 400, "InvalidScope")
			if calls != 0 {
				t.Fatal("resolver classified a malformed resource user")
			}
		})
	}

	r := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	r.Header.Set("X-Resource-User-ID", "other-user")
	_, err := resolveResourceIdentity(resolve, r)
	expectIdentityError(t, err, 403, "ScopeAccessDenied")

	calls = 0
	r.Header.Set("X-Resource-User-ID", "user")
	r.Header.Add("X-Resource-User-ID", "other-user")
	_, err = resolveResourceIdentity(resolve, r)
	expectIdentityError(t, err, 400, "InvalidScope")
	if calls != 0 {
		t.Fatal("resolver classified a repeated resource user")
	}

	unauthorized := func(*http.Request) (RequestIdentity, error) {
		return RequestIdentity{}, serrors.NewError(http.StatusUnauthorized, "AUTH_REQUIRED", "请先登录")
	}
	r = httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	r.Header.Set("X-Resource-User-ID", "user")
	_, err = resolveResourceIdentity(unauthorized, r)
	expectIdentityError(t, err, 401, "AUTH_REQUIRED")
	r.Header.Set("X-Resource-User-ID", " ")
	_, err = resolveResourceIdentity(unauthorized, r)
	expectIdentityError(t, err, 400, "InvalidScope")
}

func TestIgnoredResourceUserStaysScopeMismatch(t *testing.T) {
	resolve := func(*http.Request) (RequestIdentity, error) {
		return RequestIdentity{UserID: "user", TenantID: "tenant", OwnershipState: OwnershipActive}, nil
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	got, err := resolveResourceIdentity(resolve, r)
	if err != nil || got.ResourceUserID != nil || got.UserID != "user" {
		t.Fatalf("shared: %+v %v", got, err)
	}
	user := "user"
	personal := func(*http.Request) (RequestIdentity, error) {
		return RequestIdentity{
			UserID:         "user",
			TenantID:       "tenant",
			ResourceUserID: &user,
			OwnershipState: OwnershipActive,
		}, nil
	}
	r.Header.Set("X-Resource-User-ID", "user")
	got, err = resolveResourceIdentity(personal, r)
	if err != nil || got.ResourceUserID == nil || *got.ResourceUserID != "user" {
		t.Fatalf("personal: %+v %v", got, err)
	}
	_, err = resolveResourceIdentity(resolve, r)
	expectIdentityError(t, err, 403, "ScopeMismatch")
}

func expectIdentityError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var detail *serrors.Error
	if !errors.As(err, &detail) || detail.Status != status || detail.Code != code {
		t.Fatalf("expected %d/%s; got %v", status, code, err)
	}
}
