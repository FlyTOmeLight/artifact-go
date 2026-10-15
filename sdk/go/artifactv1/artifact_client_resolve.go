package artifactv1

import (
	"context"
	"errors"

	"github.com/FlyTOmeLight/server-go/sdk"
)

func (c *Client) Resolve(ctx context.Context, req *ResolveRequest) (*ResolveResponse, error) {
	if req == nil || req.URI == "" {
		return nil, errors.New("artifact URI is required")
	}
	var result ResolveResponse
	response, err := c.request(ctx).SetBody(req).SetResult(&result).Post("/v1/resolve")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}
