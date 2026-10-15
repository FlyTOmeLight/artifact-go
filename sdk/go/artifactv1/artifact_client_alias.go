package artifactv1

import (
	"context"
	"errors"
	"strconv"

	"github.com/FlyTOmeLight/server-go/sdk"
)

func (c *Client) PutAlias(ctx context.Context, req *PutAliasRequest) (*AliasResponse, error) {
	if req == nil || req.ArtifactID == "" || req.Name == "" {
		return nil, errors.New("artifact ID and alias name are required")
	}
	var result AliasResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("name", req.Name).
		SetBody(req).
		SetResult(&result).
		Put("/v1/artifacts/{artifactID}/aliases/{name}")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) DeleteAlias(ctx context.Context, req *DeleteAliasRequest) (*AliasResponse, error) {
	if req == nil || req.ArtifactID == "" || req.Name == "" || req.ExpectedRevision == nil {
		return nil, errors.New("artifact ID, alias name and expected revision are required")
	}
	var result AliasResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("name", req.Name).
		SetQueryParam("expectedRevision", strconv.FormatInt(*req.ExpectedRevision, 10)).
		SetResult(&result).
		Delete("/v1/artifacts/{artifactID}/aliases/{name}")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetAlias(ctx context.Context, req *GetAliasRequest) (*AliasResponse, error) {
	if req == nil || req.ArtifactID == "" || req.Name == "" {
		return nil, errors.New("artifact ID and alias name are required")
	}
	request := c.request(ctx).SetPathParam("artifactID", req.ArtifactID).SetPathParam("name", req.Name)
	if req.IncludeDeleted {
		request.SetQueryParam("includeDeleted", "true")
	}
	var result AliasResponse
	response, err := request.SetResult(&result).Get("/v1/artifacts/{artifactID}/aliases/{name}")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}
