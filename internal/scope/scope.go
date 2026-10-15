// Package scope carries authenticated request identity, never raw client headers.
package scope

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
)

type Identity struct {
	UserID    string
	TenantID  string
	ProjectID *string
	Active    bool
}

type identityKey struct{}

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func Set(c *core.Context, identity Identity) {
	if identity.ProjectID != nil {
		project := *identity.ProjectID
		identity.ProjectID = &project
	}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), identityKey{}, identity))
}

func From(c *core.Context) (Identity, error) {
	identity, ok := c.Request.Context().Value(identityKey{}).(Identity)
	if !ok || !validID(identity.UserID) || !tenantIDPattern.MatchString(identity.TenantID) ||
		(identity.ProjectID != nil && !validID(*identity.ProjectID)) {
		return Identity{}, serrors.NewError(
			http.StatusUnauthorized,
			"Unauthorized",
			"trusted request identity is required",
		)
	}
	return identity, nil
}

func validID(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= 64 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func (i Identity) Key() string {
	if i.ProjectID == nil {
		return "tenant"
	}
	return "project:" + *i.ProjectID
}

func (i Identity) RequireActive() error {
	if !i.Active {
		return serrors.NewError(http.StatusForbidden, "ScopeInactive", "tenant or project is inactive")
	}
	return nil
}
