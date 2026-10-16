package storage

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	client "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

type Controller struct {
	backend             *client.Client
	maxRequestBodyBytes int64
}

func NewController(backend *client.Client, maxRequestBodyBytes int64) *Controller {
	return &Controller{backend: backend, maxRequestBodyBytes: maxRequestBodyBytes}
}

// RegisterRoutes requires the caller to install the trusted tenant-service verifier.
func (c *Controller) RegisterRoutes(group *core.RouterGroup) {
	group.POST("/storage/tenant-buckets", c.CreateTenantBucket)
}

// CreateTenantBucket godoc
// @Summary Prepare the tenant's fixed storage bucket
// @Description Requires the injected tenant-service verifier; user scope headers are not management credentials.
// @Tags storage
// @Security TenantServiceAuth
// @Accept json
// @Produce json
// @Param request body storagev1.CreateTenantBucketRequest true "Tenant identity"
// @Success 201 {object} storagev1.TenantBucketResponse
// @Success 200 {object} storagev1.TenantBucketResponse
// @Failure 400,401,403,409,413,503 {object} errors.Error
// @Router /v1/storage/tenant-buckets [post]
func (c *Controller) CreateTenantBucket(ctx *core.Context) {
	var req *api.CreateTenantBucketRequest
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		var extra any
		if next := decoder.Decode(&extra); next != io.EOF {
			if next == nil {
				err = errors.New("multiple JSON values")
			} else {
				err = next
			}
		}
	}
	if err != nil || req == nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			serrors.AbortWithError(
				ctx,
				serrors.NewError(413, "RequestBodyTooLarge", "request body exceeds configured limit"),
			)
		} else {
			serrors.AbortWithError(
				ctx,
				serrors.NewError(
					400,
					"InvalidParameter",
					"expected one object containing only tenantID",
				),
			)
		}
		return
	}
	out, created, err := createTenantBucket(ctx.Request.Context(), c.backend, req.TenantID)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	ctx.JSON(status, out)
}
