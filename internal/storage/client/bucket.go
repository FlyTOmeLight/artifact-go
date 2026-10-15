package client

import (
	"context"
	"errors"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// backendError exposes a stable safe message while retaining the original cause
// for errors.Is/As diagnostics. The HTTP renderer selects the public error.
type backendError struct{ public, cause error }

func (e *backendError) Error() string { return e.public.Error() }

func (e *backendError) Unwrap() []error { return []error{e.public, e.cause} }

func unavailable(cause error) error {
	return &backendError{
		public: serrors.NewError(
			503,
			"StorageBucketUnavailable",
			"storage bucket access or ownership could not be confirmed",
		),
		cause: cause,
	}
}

func conflict() error {
	return serrors.NewError(
		409,
		"StorageBucketConflict",
		"storage bucket belongs to another account or its creation conflicts",
	)
}

func errorCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}

// EnsureBucket confirms both access and account ownership, including after creation races.
func (c *Client) EnsureBucket(ctx context.Context, tenantID string) (string, bool, error) {
	bucket, err := TenantBucket(tenantID)
	if err != nil {
		return "", false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return bucket, false, c.confirmOwned(ctx, bucket)
	}
	if code := errorCode(err); code != "NotFound" && code != "NoSuchBucket" {
		return "", false, unavailable(err)
	}
	input := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	if c.region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(c.region),
		}
	}
	_, err = c.s3.CreateBucket(ctx, input)
	created := err == nil
	if err != nil {
		switch errorCode(err) {
		case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
		default:
			return "", false, unavailable(err)
		}
	}
	if checkErr := c.confirmOwned(ctx, bucket); checkErr != nil {
		return "", false, checkErr
	}
	_, err = c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		return "", false, unavailable(err)
	}
	return bucket, created, nil
}

// HeadBucket alone may succeed through a public or cross-account policy. ListBuckets
// is the S3 account-owned bucket inventory and therefore supplies the ownership proof.
func (c *Client) confirmOwned(ctx context.Context, bucket string) error {
	pages := s3.NewListBucketsPaginator(
		c.s3,
		&s3.ListBucketsInput{MaxBuckets: aws.Int32(1000)},
		func(o *s3.ListBucketsPaginatorOptions) { o.StopOnDuplicateToken = true },
	)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return unavailable(err)
		}
		for _, b := range page.Buckets {
			if aws.ToString(b.Name) == bucket {
				return nil
			}
		}
	}
	return conflict()
}
