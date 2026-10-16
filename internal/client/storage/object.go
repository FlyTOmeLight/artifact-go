package client

import (
	"context"
	"errors"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type ObjectInfo struct{ Size int64 }

var ErrObjectNotFound = errors.New("storage object not found")

func objectError(cause error) error {
	return &backendError{
		public: serrors.NewError(503, "StorageUnavailable", "object storage operation failed"),
		cause:  cause,
	}
}

// CheckBucket verifies access without provisioning missing storage.
func (c *Client) CheckBucket(ctx context.Context, bucket string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		return unavailable(err)
	}
	return c.confirmOwned(ctx, bucket)
}

func (c *Client) Stat(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		if code := errorCode(err); code == "NotFound" || code == "NoSuchKey" || code == "NoSuchBucket" {
			return ObjectInfo{}, ErrObjectNotFound
		}
		return ObjectInfo{}, objectError(err)
	}
	if out.ContentLength == nil || *out.ContentLength < 0 {
		return ObjectInfo{}, objectError(errors.New("missing object size"))
	}
	return ObjectInfo{Size: *out.ContentLength}, nil
}

// Delete aborts multipart uploads for this exact key before removing the object.
// The caller must already have stopped grants and waited for upload credentials.
func (c *Client) Delete(ctx context.Context, bucket, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	seen := map[string]bool{}
	pages := s3.NewListMultipartUploadsPaginator(
		c.s3,
		&s3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String(key)},
	)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			if errorCode(err) == "NoSuchBucket" {
				return nil
			}
			return objectError(err)
		}
		if aws.ToBool(page.IsTruncated) {
			token := aws.ToString(page.NextKeyMarker) + "\x00" + aws.ToString(page.NextUploadIdMarker)
			if seen[token] {
				return objectError(errors.New("repeated multipart pagination marker"))
			}
			seen[token] = true
		}
		for _, upload := range page.Uploads {
			if aws.ToString(upload.Key) != key {
				continue
			}
			_, err = c.s3.AbortMultipartUpload(
				ctx,
				&s3.AbortMultipartUploadInput{
					Bucket:   aws.String(bucket),
					Key:      aws.String(key),
					UploadId: upload.UploadId,
				},
			)
			if err != nil && errorCode(err) != "NoSuchUpload" {
				return objectError(err)
			}
		}
	}
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		if code := errorCode(err); code == "NoSuchBucket" || code == "NoSuchKey" {
			return nil
		}
		return objectError(err)
	}
	return nil
}

// HasIncompleteUploads detects unfinished uploads without broadening the target key.
func (c *Client) HasIncompleteUploads(ctx context.Context, bucket, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	seen := map[string]bool{}
	pages := s3.NewListMultipartUploadsPaginator(
		c.s3,
		&s3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String(key)},
	)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			if errorCode(err) == "NoSuchBucket" {
				return false, nil
			}
			return false, objectError(err)
		}
		if aws.ToBool(page.IsTruncated) {
			token := aws.ToString(page.NextKeyMarker) + "\x00" + aws.ToString(page.NextUploadIdMarker)
			if seen[token] {
				return false, objectError(errors.New("repeated multipart pagination marker"))
			}
			seen[token] = true
		}
		for _, upload := range page.Uploads {
			if aws.ToString(upload.Key) == key {
				return true, nil
			}
		}
	}
	return false, nil
}
