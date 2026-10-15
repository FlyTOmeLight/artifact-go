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

func (c *Client) CreateDraft(ctx context.Context, req *CreateDraftRequest) (*DraftResponse, error) {
	if req == nil || req.ArtifactID == "" {
		return nil, errors.New("artifact ID is required")
	}
	var result DraftResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetBody(req).
		SetResult(&result).
		Post("/v1/artifacts/{artifactID}/drafts")
	if err = completeDraft(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ListDrafts(ctx context.Context, req *ListDraftsRequest) (*ListDraftsResponse, error) {
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
	var result ListDraftsResponse
	response, err := request.SetResult(&result).Get("/v1/artifacts/{artifactID}/drafts")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetDraft(ctx context.Context, req *GetDraftRequest) (*DraftResponse, error) {
	if req == nil || req.ArtifactID == "" || req.DraftID == "" {
		return nil, errors.New("artifact and draft IDs are required")
	}
	var result DraftResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("draftID", req.DraftID).
		SetResult(&result).
		Get("/v1/artifacts/{artifactID}/drafts/{draftID}")
	if err = completeDraft(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) UpdateDraft(ctx context.Context, req *UpdateDraftRequest) (*UpdateDraftResponse, error) {
	if req == nil || req.ArtifactID == "" || req.DraftID == "" {
		return nil, errors.New("artifact and draft IDs are required")
	}
	var result UpdateDraftResponse
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("draftID", req.DraftID).
		SetBody(req).
		SetResult(&result).
		Put("/v1/artifacts/{artifactID}/drafts/{draftID}")
	if err = completeDraft(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) DeleteDraft(ctx context.Context, req *DeleteDraftRequest) error {
	if req == nil || req.ArtifactID == "" || req.DraftID == "" || req.ExpectedRevision == nil {
		return errors.New("artifact ID, draft ID and expected revision are required")
	}
	response, err := c.request(ctx).
		SetPathParam("artifactID", req.ArtifactID).
		SetPathParam("draftID", req.DraftID).
		SetQueryParam("expectedRevision", strconv.FormatInt(*req.ExpectedRevision, 10)).
		Delete("/v1/artifacts/{artifactID}/drafts/{draftID}")
	return sdk.Complete(response, err)
}

func completeDraft(response *resty.Response, transportError error) error {
	err := sdk.Complete(response, transportError)
	if transportError != nil || response == nil || response.StatusCode() != http.StatusGone {
		return err
	}
	var serviceError *frameworkerrors.Error
	if !errors.As(err, &serviceError) || serviceError.Code != CodeDraftDeleted {
		return err
	}
	var deleted DraftDeletedResponse
	if json.Unmarshal(response.Bytes(), &deleted) != nil || deleted.Draft.ID == "" {
		return err
	}
	return &DraftDeletedError{Cause: err, Draft: deleted.Draft, AppliedRevision: deleted.AppliedRevision}
}
