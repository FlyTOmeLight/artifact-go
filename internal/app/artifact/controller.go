package artifact

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/creasty/defaults"
	"github.com/gin-gonic/gin/binding"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

// @title Artifact API
// @version 0.1.0
// @description Collections, JSON versions and drafts, aliases, conditional management and retention. Scope headers select a scope verified by the injected identity resolver.
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Bearer user token; scope headers are not identity proof.
// @securityDefinitions.apikey TenantServiceAuth
// @in header
// @name Authorization
// @description Trusted tenant-system credential, checked by the injected TenantServiceVerifier.
type Controller struct {
	service             *Service
	maxRequestBodyBytes int64
}

func NewController(service *Service, maxRequestBodyBytes int64) *Controller {
	return &Controller{service: service, maxRequestBodyBytes: maxRequestBodyBytes}
}

func (c *Controller) RegisterRoutes(group *core.RouterGroup) {
	group.POST("/artifacts", c.Create)
	group.GET("/artifacts", c.List)
	group.GET("/artifacts/:artifactID", c.Get)
	group.POST("/artifacts/:artifactID/archive", c.Archive)
}

// Create godoc
// @Summary Create an artifact collection
// @Tags artifacts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection; omit for tenant scope"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param X-Request-ID header string false "Request correlation ID"
// @Accept json
// @Produce json
// @Param request body artifactv1.CreateArtifactRequest true "Collection"
// @Success 201 {object} artifactv1.ArtifactResponse
// @Success 200 {object} artifactv1.ArtifactResponse "Successful replay with current collection state"
// @Failure 400,401,403,409,413,500,503 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts [post]
func (c *Controller) Create(ctx *core.Context) {
	var req *api.CreateArtifactRequest
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		var extra any
		if next := decoder.Decode(&extra); next != io.EOF {
			if next == nil {
				err = errors.New("expected one JSON object")
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
			serrors.AbortWithError(ctx, invalid("expected one valid JSON object"))
		}
		return
	}
	if req == nil {
		serrors.AbortWithError(ctx, invalid("request must be an object"))
		return
	}
	if err := binding.Validator.ValidateStruct(req); err != nil {
		serrors.AbortWithError(ctx, invalid(err.Error()))
		return
	}
	out, created, err := c.service.Create(ctx, req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.Set("artifact.response.artifactID", out.ID)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	ctx.JSON(status, out)
}

// List godoc
// @Summary List collections in the exact request scope
// @Tags artifacts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection; omit for tenant scope"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param X-Request-ID header string false "Request correlation ID"
// @Produce json
// @Param type query string false "Artifact type"
// @Param state query string false "State" Enums(active,archived)
// @Param cursor query string false "Query-bound cursor"
// @Param limit query int false "Page size" default(50) minimum(1) maximum(200)
// @Success 200 {object} artifactv1.ListArtifactsResponse
// @Failure 400,401,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts [get]
func (c *Controller) List(ctx *core.Context) {
	var req api.ListArtifactsRequest
	if err := defaults.Set(&req); err != nil {
		serrors.AbortWithError(ctx, serrors.ErrorInternalError)
		return
	}
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	out, err := c.service.List(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Get godoc
// @Summary Read a collection, including archived collections
// @Tags artifacts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection; omit for tenant scope"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param X-Request-ID header string false "Request correlation ID"
// @Produce json
// @Param artifactID path string true "Artifact ID"
// @Success 200 {object} artifactv1.ArtifactResponse
// @Failure 400,401,404,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID} [get]
func (c *Controller) Get(ctx *core.Context) {
	var req api.GetArtifactRequest
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	out, err := c.service.Get(ctx, req.ArtifactID)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Archive godoc
// @Summary Archive a collection while retaining its identity
// @Tags artifacts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection; omit for tenant scope"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param X-Request-ID header string false "Request correlation ID"
// @Produce json
// @Param artifactID path string true "Artifact ID"
// @Success 200 {object} artifactv1.ArtifactResponse
// @Failure 400,401,404,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/archive [post]
func (c *Controller) Archive(ctx *core.Context) {
	var req api.ArchiveArtifactRequest
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	out, err := c.service.Archive(ctx, req.ArtifactID)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}
