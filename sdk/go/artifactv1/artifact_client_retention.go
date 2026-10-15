package artifactv1

import (
	"context"
	"errors"

	"github.com/FlyTOmeLight/server-go/sdk"
)

func (c *Client) UpdateRetention(ctx context.Context, req *UpdateRetentionRequest) (*RetentionResponse, error) {
	if req == nil || req.ArtifactID == "" {
		return nil, errors.New("artifact ID is required")
	}
	var result RetentionResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetBody(req).
		SetResult(&result).
		Put("/v1/artifacts/{artifactID}/retention-policy")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) PreviewRetention(
	ctx context.Context,
	req *PreviewRetentionRequest,
) (*PreviewRetentionResponse, error) {
	if req == nil || req.ArtifactID == "" {
		return nil, errors.New("artifact ID is required")
	}
	var result PreviewRetentionResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetResult(&result).
		Post("/v1/artifacts/{artifactID}/retention-policy/preview")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}
