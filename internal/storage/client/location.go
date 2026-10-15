package client

import (
	"regexp"

	serrors "github.com/FlyTOmeLight/server-go/errors"
)

var tenantPattern = regexp.MustCompile(`^t_[0-9a-f]{24}$`)

// TenantBucket implements the fixed v1 mapping without changing the tenant identity.
func TenantBucket(tenantID string) (string, error) {
	if !tenantPattern.MatchString(tenantID) {
		return "", serrors.NewError(
			400,
			"InvalidParameter",
			"tenantID must be t_ followed by 24 lowercase hexadecimal characters",
		)
	}
	return "tenant-t-" + tenantID[2:], nil
}
