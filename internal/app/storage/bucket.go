package storage

import (
	"context"

	client "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

func createTenantBucket(
	ctx context.Context,
	backend *client.Client,
	tenantID string,
) (*api.TenantBucketResponse, bool, error) {
	bucket, created, err := backend.EnsureBucket(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	return &api.TenantBucketResponse{
		TenantID:  tenantID,
		BackendID: backend.BackendID(),
		Bucket:    bucket,
	}, created, nil
}
