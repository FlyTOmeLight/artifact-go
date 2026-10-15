package artifactv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	frameworkerrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/sdk"
	"resty.dev/v3"
)

func (c *Client) CreateVersion(ctx context.Context, req *CreateVersionRequest) (*VersionResponse, error) {
	if req == nil || req.ArtifactID == "" {
		return nil, errors.New("artifact ID is required")
	}
	var result VersionResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetBody(req).
		SetResult(&result).
		Post("/v1/artifacts/{artifactID}/versions")
	if err = completeVersion(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ListVersions(ctx context.Context, req *ListVersionsRequest) (*ListVersionsResponse, error) {
	if req == nil || req.ArtifactID == "" {
		return nil, errors.New("artifact ID is required")
	}
	request := c.request(ctx).SetPathParam("artifactID", req.ArtifactID)
	if req.Limit != 0 {
		request.SetQueryParam("limit", strconv.Itoa(req.Limit))
	}
	if req.Cursor != "" {
		request.SetQueryParam("cursor", req.Cursor)
	}
	if req.Number != "" {
		request.SetQueryParam("number", req.Number)
	}
	var result ListVersionsResponse
	response, err := request.SetResult(&result).Get("/v1/artifacts/{artifactID}/versions")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetVersion(ctx context.Context, req *GetVersionRequest) (*VersionResponse, error) {
	if req == nil || req.ArtifactID == "" || req.VersionID == "" {
		return nil, errors.New("artifact and version IDs are required")
	}
	var result VersionResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("versionID", req.VersionID).
		SetResult(&result).
		Get("/v1/artifacts/{artifactID}/versions/{versionID}")
	if err = completeVersion(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func completeVersion(response *resty.Response, transportError error) error {
	err := sdk.Complete(response, transportError)
	if transportError != nil || response == nil || response.StatusCode() != http.StatusGone {
		return err
	}
	var serviceError *frameworkerrors.Error
	if !errors.As(err, &serviceError) || serviceError.Code != CodeVersionDeleted {
		return err
	}
	var deleted VersionDeletedResponse
	if json.Unmarshal(response.Bytes(), &deleted) != nil || deleted.Version.ID == "" {
		return err
	}
	return &VersionDeletedError{Cause: err, Version: deleted.Version}
}

func (c *Client) UpdateAnnotations(ctx context.Context, req *UpdateAnnotationsRequest) (*VersionResponse, error) {
	if req == nil || req.ArtifactID == "" || req.VersionID == "" {
		return nil, errors.New("artifact and version IDs are required")
	}
	var result VersionResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("versionID", req.VersionID).
		SetBody(req).
		SetResult(&result).
		Patch("/v1/artifacts/{artifactID}/versions/{versionID}/annotations")
	if err = completeVersion(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) UpdateProtection(ctx context.Context, req *UpdateProtectionRequest) (*VersionResponse, error) {
	if req == nil || req.ArtifactID == "" || req.VersionID == "" {
		return nil, errors.New("artifact and version IDs are required")
	}
	var result VersionResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("versionID", req.VersionID).
		SetBody(req).
		SetResult(&result).
		Patch("/v1/artifacts/{artifactID}/versions/{versionID}/protection")
	if err = completeVersion(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) DeleteVersion(ctx context.Context, req *DeleteVersionRequest) (*DeleteVersionResponse, error) {
	if req == nil || req.ArtifactID == "" || req.VersionID == "" {
		return nil, errors.New("artifact ID and version ID are required")
	}
	var result DeleteVersionResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("versionID", req.VersionID).
		SetResult(&result).
		Delete("/v1/artifacts/{artifactID}/versions/{versionID}")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}
