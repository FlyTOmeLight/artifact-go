package retention

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type Controller struct{ service *Service }

func NewController(s *Service) *Controller { return &Controller{service: s} }

func (c *Controller) RegisterRoutes(group *core.RouterGroup) {
	group.PUT("/artifacts/:artifactID/retention-policy", c.Update)
	group.POST("/artifacts/:artifactID/retention-policy/preview", c.Preview)
}

// Update godoc
// @Summary Replace the retention policy using its current revision
// @Tags retention
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Accept json
// @Produce json
// @Param request body artifactv1.UpdateRetentionRequest true "Complete policy; maxVersions and maxAgeSeconds must be present and may be null"
// @Success 200 {object} artifactv1.RetentionResponse
// @Failure 400,401,404,409,413,500,503 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/retention-policy [put]
func (c *Controller) Update(ctx *core.Context) {
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.service.maxRequestBodyBytes))
	err := dec.Decode(&fields)
	if err == nil {
		var extra any
		if next := dec.Decode(&extra); next != io.EOF {
			if next == nil {
				err = errors.New("multiple JSON values")
			} else {
				err = next
			}
		}
	}
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			serrors.AbortWithError(
				ctx,
				serrors.NewError(413, "RequestBodyTooLarge", "request body exceeds configured limit"),
			)
		} else {
			serrors.AbortWithError(ctx, invalid("expected one JSON object"))
		}
		return
	}
	required := []string{
		"expectedRevision",
		"enabled",
		"maxVersions",
		"maxAgeSeconds",
		"minVersions",
		"overflowAction",
		"protectAliases",
	}
	if len(fields) != len(required) {
		serrors.AbortWithError(
			ctx,
			invalid("complete policy fields are required; unknown fields are not allowed"),
		)
		return
	}
	for _, name := range required {
		raw, ok := fields[name]
		if !ok ||
			(name != "maxVersions" && name != "maxAgeSeconds" && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))) {
			serrors.AbortWithError(ctx, invalid("missing or null policy field: "+name))
			return
		}
	}
	body, err := json.Marshal(fields)
	if err != nil {
		serrors.AbortWithError(ctx, invalid("invalid policy"))
		return
	}
	var req api.UpdateRetentionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		serrors.AbortWithError(ctx, invalid("invalid policy field types"))
		return
	}
	req.ArtifactID = ctx.Param("artifactID")
	out, err := c.service.Update(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Preview godoc
// @Summary Preview retention candidates without changing resources
// @Tags retention
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Produce json
// @Success 200 {object} artifactv1.PreviewRetentionResponse
// @Failure 400,401,404,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/retention-policy/preview [post]
func (c *Controller) Preview(ctx *core.Context) {
	var req api.PreviewRetentionRequest
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	out, err := c.service.Preview(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}
