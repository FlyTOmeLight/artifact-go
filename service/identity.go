package service

import (
	"net/http"
	"strings"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
)

// OwnershipState describes the lifecycle of the already authorized tenant/project.
type OwnershipState string

const (
	OwnershipActive  OwnershipState = "active"
	OwnershipDeleted OwnershipState = "deleted"
)

// RequestIdentity is produced by the integration's authentication boundary.
// ProjectID=nil selects the tenant itself; ResourceUserID=nil selects shared data.
// UserID is the actual actor; ResourceUserID is separately authorized resource ownership.
type RequestIdentity struct {
	UserID         string
	TenantID       string
	ProjectID      *string
	ResourceUserID *string
	OwnershipState OwnershipState
}

// IdentityResolver authenticates a request and verifies its exact selected scope.
// Untrusted request headers alone are not a valid implementation.
type IdentityResolver func(*http.Request) (RequestIdentity, error)

func identityMiddleware(resolve IdentityResolver) core.HandlerFunc {
	return func(ctx *core.Context) {
		identity, err := resolveResourceIdentity(resolve, ctx.Request)
		if err != nil {
			serrors.AbortWithError(ctx, err)
			return
		}
		if strings.TrimSpace(identity.UserID) == "" || strings.TrimSpace(identity.TenantID) == "" ||
			(identity.ProjectID != nil && strings.TrimSpace(*identity.ProjectID) == "") {
			serrors.Abort(
				ctx,
				http.StatusUnauthorized,
				"InvalidIdentity",
				"verified user and exact scope are required",
			)
			return
		}
		if identity.OwnershipState != OwnershipActive && identity.OwnershipState != OwnershipDeleted {
			serrors.Abort(
				ctx,
				http.StatusUnauthorized,
				"InvalidIdentity",
				"verified ownership state is required",
			)
			return
		}
		scope.Set(
			ctx,
			scope.Identity{
				UserID:         identity.UserID,
				TenantID:       identity.TenantID,
				ProjectID:      identity.ProjectID,
				ResourceUserID: identity.ResourceUserID,
				Active:         identity.OwnershipState == OwnershipActive,
			},
		)
		ctx.Next()
	}
}

// TenantServiceVerifier authenticates the trusted tenant system for bucket provisioning.
// Implementations are supplied by the integration, never inferred from user headers.
type TenantServiceVerifier func(*http.Request) error

func tenantServiceMiddleware(verify TenantServiceVerifier) core.HandlerFunc {
	return func(ctx *core.Context) {
		if err := verify(ctx.Request); err != nil {
			serrors.AbortWithError(ctx, err)
			return
		}
		ctx.Next()
	}
}

// resolveResourceIdentity refuses scope selections ignored by an older resolver.
func resolveResourceIdentity(resolve IdentityResolver, r *http.Request) (RequestIdentity, error) {
	identity, err := resolve(r)
	if err != nil {
		return RequestIdentity{}, err
	}
	values, present := r.Header[http.CanonicalHeaderKey("X-Resource-User-ID")]
	if present &&
		(len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.TrimSpace(values[0]) != values[0]) {
		return RequestIdentity{}, serrors.NewError(400, "InvalidScope", "resource user must be a nonempty ID")
	}
	selected := r.Header.Get("X-Resource-User-ID")
	if (selected == "") != (identity.ResourceUserID == nil) ||
		(identity.ResourceUserID != nil && selected != *identity.ResourceUserID) {
		return RequestIdentity{}, serrors.NewError(
			403,
			"ScopeMismatch",
			"resource user does not match the authorized scope",
		)
	}
	return identity, nil
}
