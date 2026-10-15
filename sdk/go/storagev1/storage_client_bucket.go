package storagev1

import (
	"context"
	"errors"

	"github.com/FlyTOmeLight/server-go/sdk"
)

// CreateTenantBucket prepares the tenant's bucket and returns the same location on retry.
// It requires a trusted tenant-service credential supplied through WithRequestIdentity.
func (c *Client) CreateTenantBucket(
	ctx context.Context,
	req *CreateTenantBucketRequest,
) (*TenantBucketResponse, error) {
	if req == nil {
		return nil, errors.New("create tenant bucket request is required")
	}
	var result TenantBucketResponse
	response, err := c.request(ctx).SetBody(req).SetResult(&result).Post("/v1/storage/tenant-buckets")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}
