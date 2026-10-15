package storagev1

import (
	"context"

	"github.com/FlyTOmeLight/server-go/sdk"
)

// CreateGrant obtains one temporary credential for the requested scope and files.
// It does not upload bytes, schedule multipart operations, or retry signing.
func (c *Client) CreateGrant(ctx context.Context, request *CreateGrantRequest) (*GrantResponse, error) {
	var result GrantResponse
	response, err := c.request(ctx).SetBody(request).SetResult(&result).Post("/v1/storage/grants")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}
