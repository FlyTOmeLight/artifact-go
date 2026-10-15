package version

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/creasty/defaults"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type Controller struct{ service *Service }

func NewController(service *Service) *Controller { return &Controller{service: service} }

func (c *Controller) RegisterRoutes(group *core.RouterGroup) {
	group.POST("/artifacts/:artifactID/versions", c.Create)
	group.GET("/artifacts/:artifactID/versions", c.List)
	group.GET("/artifacts/:artifactID/versions/:versionID", c.Get)
	group.DELETE("/artifacts/:artifactID/versions/:versionID", c.Delete)
	group.PATCH("/artifacts/:artifactID/versions/:versionID/annotations", c.UpdateAnnotations)
	group.PATCH("/artifacts/:artifactID/versions/:versionID/protection", c.UpdateProtection)
}

// Create godoc
// @Summary Publish JSON and files directly or from a specified draft revision
// @Tags versions
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param X-Request-ID header string false "Request correlation ID"
// @Param artifactID path string true "Artifact ID"
// @Accept json
// @Produce json
// @Param request body artifactv1.CreateVersionRequest true "Publish JSON and a complete file manifest directly or from a draft revision"
// @Success 201,200 {object} artifactv1.VersionResponse
// @Failure 400,401,403,404,409,413,500,503 {object} artifactv1.ErrorResponse
// @Failure 410 {object} artifactv1.VersionDeletedResponse
// @Router /v1/artifacts/{artifactID}/versions [post]
func (c *Controller) Create(ctx *core.Context) {
	var raw json.RawMessage
	decoder := json.NewDecoder(
		http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.service.limits.MaxRequestBodyBytes),
	)
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
			serrors.AbortWithError(ctx, invalid("expected one valid JSON object"))
		}
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		serrors.AbortWithError(ctx, invalid("request must be an object"))
		return
	}
	// Match the public JSON names exactly. Struct decoding alone also accepts
	// case-folded names, which would bypass presence/null validation below.
	for name := range fields {
		switch name {
		case "requestKey",
			"payload",
			"annotations",
			"bump",
			"files",
			"protected",
			"aliases",
			"draftID",
			"expectedDraftRevision":
		default:
			serrors.AbortWithError(ctx, invalid("unknown version request field: "+name))
			return
		}
	}
	for _, name := range []string{"files", "draftID", "expectedDraftRevision"} {
		if value, ok := fields[name]; ok && string(value) == "null" {
			serrors.AbortWithError(ctx, invalid(name+" cannot be null"))
			return
		}
	}
	if _, draft := fields["draftID"]; draft {
		for _, name := range []string{"payload", "annotations", "files"} {
			if _, present := fields[name]; present {
				serrors.AbortWithError(ctx, invalid("draft publication cannot include "+name))
				return
			}
		}
	}
	if value, ok := fields["protected"]; ok && string(value) == "null" {
		serrors.AbortWithError(ctx, invalid("protected cannot be null"))
		return
	}
	if value, ok := fields["aliases"]; ok {
		var updates []map[string]json.RawMessage
		if json.Unmarshal(value, &updates) != nil || string(value) == "null" {
			serrors.AbortWithError(ctx, invalid("aliases must be an array"))
			return
		}
		for _, update := range updates {
			for name := range update {
				if name != "name" && name != "expectedRevision" {
					serrors.AbortWithError(ctx, invalid("unknown alias request field: "+name))
					return
				}
			}
		}
	}
	if value, ok := fields["files"]; ok {
		var files []map[string]json.RawMessage
		if json.Unmarshal(value, &files) != nil {
			serrors.AbortWithError(ctx, invalid("files must be an array of objects"))
			return
		}
		for _, file := range files {
			if file == nil {
				serrors.AbortWithError(ctx, invalid("file must be an object"))
				return
			}
			for name := range file {
				if name != "path" && name != "fileID" {
					serrors.AbortWithError(ctx, invalid("unknown file reference field"))
					return
				}
			}
		}
	}
	var req api.CreateVersionRequest
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&req); err != nil {
		serrors.AbortWithError(ctx, invalid("invalid version request fields"))
		return
	}
	req.ArtifactID = ctx.Param("artifactID")
	result, replayed, err := c.service.Create(ctx, &req)
	if err != nil {
		var deleted *deletedError
		if errors.As(err, &deleted) {
			problem := serrors.Error{
				Status:    410,
				Code:      "VersionDeleted",
				Message:   "version is deleted",
				RequestID: ctx.GetHeader("X-Request-ID"),
			}
			problem.SetStatusCode(ctx.GetInt("server.AppCode"))
			ctx.Abort()
			ctx.JSON(410, api.VersionDeletedResponse{ErrorResponse: problem, Version: deleted.identity})
			return
		}
		var appErr *serrors.Error
		if errors.As(err, &appErr) && appErr.Code == "RequestInProgress" {
			ctx.Header("Retry-After", "1")
		}
		serrors.AbortWithError(ctx, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	ctx.Set("artifact.response.versionID", result.ID)
	ctx.JSON(status, result)
}

// List godoc
// @Summary List ready versions in descending sequence order
// @Tags versions
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param artifactID path string true "Artifact ID"
// @Param number query string false "Exact published version number"
// @Param cursor query string false "Query-bound cursor"
// @Param limit query int false "Page size" default(50) minimum(1) maximum(200)
// @Produce json
// @Success 200 {object} artifactv1.ListVersionsResponse
// @Failure 400,401,404,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/versions [get]
func (c *Controller) List(ctx *core.Context) {
	var req api.ListVersionsRequest
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
// @Summary Read a ready version
// @Tags versions
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param artifactID path string true "Artifact ID"
// @Param versionID path string true "Version ID"
// @Produce json
// @Success 200 {object} artifactv1.VersionResponse
// @Failure 400,401,404,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/versions/{versionID} [get]
func (c *Controller) Get(ctx *core.Context) {
	var req api.GetVersionRequest
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

// decodeManagement validates exact field names before Go's case-insensitive struct decoder.
func (c *Controller) decodeManagement(ctx *core.Context, target any, field string) bool {
	decoder := json.NewDecoder(
		http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.service.limits.MaxRequestBodyBytes),
	)
	var raw json.RawMessage
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
			serrors.AbortWithError(ctx, invalid("expected one valid JSON object"))
		}
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		serrors.AbortWithError(ctx, invalid("request must be an object"))
		return false
	}
	for name := range fields {
		if name != "expectedRevision" && name != field {
			serrors.AbortWithError(ctx, invalid("unknown version request field: "+name))
			return false
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		serrors.AbortWithError(ctx, invalid("invalid version request fields"))
		return false
	}
	return true
}

// UpdateAnnotations godoc
// @Summary Replace version annotations with revision checking
// @Tags versions
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param artifactID path string true "Artifact ID"
// @Param versionID path string true "Version ID"
// @Accept json
// @Produce json
// @Param request body artifactv1.UpdateAnnotationsRequest true "Complete annotations and expected revision"
// @Success 200 {object} artifactv1.VersionResponse
// @Failure 400,401,404,409,413,500,503 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/versions/{versionID}/annotations [patch]
func (c *Controller) UpdateAnnotations(ctx *core.Context) {
	var req api.UpdateAnnotationsRequest
	if !c.decodeManagement(ctx, &req, "annotations") {
		return
	}
	req.ArtifactID = ctx.Param("artifactID")
	req.VersionID = ctx.Param("versionID")
	out, err := c.service.UpdateAnnotations(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// UpdateProtection godoc
// @Summary Set version protection with revision checking
// @Tags versions
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param artifactID path string true "Artifact ID"
// @Param versionID path string true "Version ID"
// @Accept json
// @Produce json
// @Param request body artifactv1.UpdateProtectionRequest true "Explicit protection and expected revision"
// @Success 200 {object} artifactv1.VersionResponse
// @Failure 400,401,404,409,413,500,503 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/versions/{versionID}/protection [patch]
func (c *Controller) UpdateProtection(ctx *core.Context) {
	var req api.UpdateProtectionRequest
	if !c.decodeManagement(ctx, &req, "protected") {
		return
	}
	req.ArtifactID = ctx.Param("artifactID")
	req.VersionID = ctx.Param("versionID")
	out, err := c.service.UpdateProtection(ctx, &req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Delete godoc
// @Summary Delete a version and retain its identity tombstone
// @Tags versions
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param artifactID path string true "Artifact ID"
// @Param versionID path string true "Version ID"
// @Produce json
// @Success 200 {object} artifactv1.DeleteVersionResponse
// @Failure 400,401,404,409,500,503 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/versions/{versionID} [delete]
func (c *Controller) Delete(ctx *core.Context) {
	var req api.DeleteVersionRequest
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
