package storagev1

import (
	"context"
	"net/url"

	"github.com/FlyTOmeLight/server-go/sdk"
)

// RegisterFiles reserves independent files using per-item idempotency keys.
// HTTP success may contain individual failures; inspect every result's Error.
func (c *Client) RegisterFiles(ctx context.Context, request RegisterFilesRequest) (RegisterFilesResponse, error) {
	var result RegisterFilesResponse
	response, err := c.request(ctx).SetBody(request).SetResult(&result).Post("/v1/storage/files")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return result, nil
}

// GetFile reads metadata in the current request's trusted tenant/project scope.
func (c *Client) GetFile(ctx context.Context, fileID string) (*FileResponse, error) {
	var result FileResponse
	response, err := c.request(ctx).SetResult(&result).Get("/v1/storage/files/" + url.PathEscape(fileID))
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}

// CompleteFiles confirms existence and size; it does not validate declared checksums.
func (c *Client) CompleteFiles(ctx context.Context, request CompleteFilesRequest) (CompleteFilesResponse, error) {
	var result CompleteFilesResponse
	response, err := c.request(ctx).SetBody(request).SetResult(&result).Post("/v1/storage/files/complete")
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return result, nil
}

// DeleteFile schedules deletion of an unbound file, or returns its deleted tombstone.
func (c *Client) DeleteFile(ctx context.Context, fileID string) (*FileResponse, error) {
	var result FileResponse
	response, err := c.request(ctx).SetResult(&result).Delete("/v1/storage/files/" + url.PathEscape(fileID))
	if err = sdk.Complete(response, err); err != nil {
		return nil, err
	}
	return &result, nil
}
