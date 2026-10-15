package artifactv1

import (
	"context"
	"errors"
	"strconv"

	"github.com/FlyTOmeLight/server-go/sdk"
)

func (c *Client) CreateArtifact(ctx context.Context, req *CreateArtifactRequest) (*ArtifactResponse, error) {
	if req == nil {
		return nil, errors.New("create artifact request is required")
	}
	var result ArtifactResponse
	response, err := c.request(ctx).SetBody(req).SetResult(&result).Post("/v1/artifacts")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ListArtifacts(ctx context.Context, req *ListArtifactsRequest) (*ListArtifactsResponse, error) {
	if req == nil {
		return nil, errors.New("list artifacts request is required")
	}
	request := c.request(ctx)
	if req.Limit != 0 {
		request.SetQueryParam("limit", strconv.Itoa(req.Limit))
	}
	if req.Cursor != "" {
		request.SetQueryParam("cursor", req.Cursor)
	}
	if req.Type != "" {
		request.SetQueryParam("type", req.Type)
	}
	if req.State != "" {
		request.SetQueryParam("state", string(req.State))
	}
	var result ListArtifactsResponse
	response, err := request.SetResult(&result).Get("/v1/artifacts")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetArtifact(ctx context.Context, req *GetArtifactRequest) (*ArtifactResponse, error) {
	if req == nil || req.ArtifactID == "" {
		return nil, errors.New("artifact ID is required")
	}
	var result ArtifactResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetResult(&result).
		Get("/v1/artifacts/{artifactID}")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ArchiveArtifact(ctx context.Context, req *ArchiveArtifactRequest) (*ArtifactResponse, error) {
	if req == nil || req.ArtifactID == "" {
		return nil, errors.New("artifact ID is required")
	}
	var result ArtifactResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetResult(&result).
		Post("/v1/artifacts/{artifactID}/archive")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}
