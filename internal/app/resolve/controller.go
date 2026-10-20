package resolve

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type Controller struct{ service *Service }

func NewController(service *Service) *Controller { return &Controller{service: service} }

func (c *Controller) RegisterRoutes(group *core.RouterGroup) { group.POST("/resolve", c.Resolve) }

// Resolve godoc
// @Summary Resolve an artifact URI to visible business metadata
// @Tags resolve
// @Security BearerAuth
// @Param x-forge-tenant-id header string true "Tenant scope selection"
// @Param x-forge-project-id header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param X-Request-ID header string false "Request correlation ID"
// @Accept json
// @Produce json
// @Param request body artifactv1.ResolveRequest true "Persistent URI"
// @Success 200 {object} artifactv1.ResolveResponse
// @Failure 400,401,403,404,413,500 {object} artifactv1.ErrorResponse
// @Router /v1/resolve [post]
func (c *Controller) Resolve(ctx *core.Context) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.service.maxRequestBodyBytes))
	err := decoder.Decode(&raw)
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
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			serrors.AbortWithError(
				ctx,
				serrors.NewError(413, "RequestBodyTooLarge", "request body exceeds configured limit"),
			)
		} else {
			serrors.AbortWithError(
				ctx,
				serrors.NewError(400, "InvalidResolveRequest", "expected one JSON object"),
			)
		}
		return
	}
	var req api.ResolveRequest
	value, ok := raw["uri"]
	if len(raw) != 1 || !ok || json.Unmarshal(value, &req.URI) != nil || req.URI == "" {
		serrors.AbortWithError(
			ctx,
			serrors.NewError(
				400,
				"InvalidResolveRequest",
				"uri must be a nonempty string and the only request field",
			),
		)
		return
	}
	out, err := c.service.Resolve(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, out)
}
