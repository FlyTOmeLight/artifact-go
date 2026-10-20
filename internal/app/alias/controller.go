package alias

import (
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

func NewController(service *Service) *Controller { return &Controller{service: service} }

func (c *Controller) RegisterRoutes(group *core.RouterGroup) {
	group.PUT("/artifacts/:artifactID/aliases/:name", c.Put)
	group.GET("/artifacts/:artifactID/aliases/:name", c.Get)
	group.DELETE("/artifacts/:artifactID/aliases/:name", c.Delete)
}

// Put godoc
// @Summary Conditionally create, move or restore an alias
// @Tags aliases
// @Security BearerAuth
// @Param x-forge-tenant-id header string true "Tenant scope selection"
// @Param x-forge-project-id header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Param name path string true "Alias name; latest is read-only"
// @Accept json
// @Produce json
// @Param request body artifactv1.PutAliasRequest true "Target and current alias revision"
// @Success 200 {object} artifactv1.AliasResponse
// @Failure 400,401,404,409,413,500,503 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/aliases/{name} [put]
func (c *Controller) Put(ctx *core.Context) {
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
			serrors.AbortWithError(ctx, invalid("expected one valid JSON object"))
		}
		return
	}
	if fields == nil {
		serrors.AbortWithError(ctx, invalid("request must be an object"))
		return
	}
	for name := range fields {
		if name != "versionID" && name != "expectedRevision" {
			serrors.AbortWithError(ctx, invalid("unknown alias request field: "+name))
			return
		}
	}
	var req api.PutAliasRequest
	if json.Unmarshal(fields["versionID"], &req.VersionID) != nil ||
		json.Unmarshal(fields["expectedRevision"], &req.ExpectedRevision) != nil {
		serrors.AbortWithError(ctx, invalid("versionID and expectedRevision are required"))
		return
	}
	req.ArtifactID = ctx.Param("artifactID")
	req.Name = ctx.Param("name")
	out, err := c.service.Put(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Get godoc
// @Summary Read an alias or the current latest ready version
// @Tags aliases
// @Security BearerAuth
// @Param x-forge-tenant-id header string true "Tenant scope selection"
// @Param x-forge-project-id header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Param name path string true "Alias name"
// @Param includeDeleted query bool false "Include alias tombstone" default(false)
// @Produce json
// @Success 200 {object} artifactv1.AliasResponse
// @Failure 400,401,404,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/aliases/{name} [get]
func (c *Controller) Get(ctx *core.Context) {
	var req api.GetAliasRequest
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	out, err := c.service.Get(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Delete godoc
// @Summary Conditionally delete an alias and retain its revision tombstone
// @Tags aliases
// @Security BearerAuth
// @Param x-forge-tenant-id header string true "Tenant scope selection"
// @Param x-forge-project-id header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Param name path string true "Alias name; latest is read-only"
// @Param expectedRevision query int true "Current alias revision" minimum(0)
// @Produce json
// @Success 200 {object} artifactv1.AliasResponse
// @Failure 400,401,404,409,500,503 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/aliases/{name} [delete]
func (c *Controller) Delete(ctx *core.Context) {
	var req api.DeleteAliasRequest
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	out, err := c.service.Delete(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}
