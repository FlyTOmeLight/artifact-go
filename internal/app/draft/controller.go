package draft

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

func (c *Controller) RegisterRoutes(g *core.RouterGroup) {
	g.POST("/artifacts/:artifactID/drafts", c.Create)
	g.GET("/artifacts/:artifactID/drafts", c.List)
	g.GET("/artifacts/:artifactID/drafts/:draftID", c.Get)
	g.PUT("/artifacts/:artifactID/drafts/:draftID", c.Update)
	g.DELETE("/artifacts/:artifactID/drafts/:draftID", c.Delete)
}

func (c *Controller) decode(ctx *core.Context, target any, update bool) bool {
	decoder := json.NewDecoder(
		http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.service.limits.MaxRequestBodyBytes),
	)
	var raw json.RawMessage
	err := decoder.Decode(&raw)
	if err == nil {
		var extra any
		if e := decoder.Decode(&extra); e != io.EOF {
			if e == nil {
				err = errors.New("multiple JSON values")
			} else {
				err = e
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
			serrors.AbortWithError(ctx, invalid("expected one JSON object"))
		}
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		serrors.AbortWithError(ctx, invalid("request must be an object"))
		return false
	}
	for name, value := range fields {
		allowed := name == "requestKey" || name == "payload" || name == "annotations" || name == "files" ||
			(!update && name == "sourceVersionID") ||
			(update && name == "expectedRevision")
		if !allowed || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			serrors.AbortWithError(ctx, invalid("unknown or null draft request field: "+name))
			return false
		}
	}
	if _, source := fields["sourceVersionID"]; source {
		for _, name := range []string{"payload", "annotations", "files"} {
			if _, present := fields[name]; present {
				serrors.AbortWithError(
					ctx,
					invalid("sourceVersionID cannot be combined with direct fields"),
				)
				return false
			}
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		serrors.AbortWithError(ctx, invalid("invalid draft request fields"))
		return false
	}
	return true
}

func abort(ctx *core.Context, err error) {
	var gone *deletedError
	if errors.As(err, &gone) {
		problem := serrors.Error{
			Status:    410,
			Code:      "DraftDeleted",
			Message:   "draft is deleted",
			RequestID: ctx.GetHeader("X-Request-ID"),
		}
		problem.SetStatusCode(ctx.GetInt("server.AppCode"))
		ctx.Abort()
		ctx.JSON(
			410,
			api.DraftDeletedResponse{
				ErrorResponse:   problem,
				Draft:           gone.identity,
				AppliedRevision: gone.applied,
			},
		)
		return
	}
	var problem *serrors.Error
	if errors.As(err, &problem) && problem.Code == "RequestInProgress" {
		ctx.Header("Retry-After", "1")
	}
	serrors.AbortWithError(ctx, err)
}

// Create godoc
// @Summary Create an independent JSON and file draft or replay its creation
// @Tags drafts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Accept json
// @Produce json
// @Param request body artifactv1.CreateDraftRequest true "Direct JSON and files or source version"
// @Success 201,200 {object} artifactv1.DraftResponse
// @Failure 400,401,403,404,409,413,500,503 {object} artifactv1.ErrorResponse
// @Failure 410 {object} artifactv1.DraftDeletedResponse
// @Router /v1/artifacts/{artifactID}/drafts [post]
func (c *Controller) Create(ctx *core.Context) {
	var req api.CreateDraftRequest
	if !c.decode(ctx, &req, false) {
		return
	}
	req.ArtifactID = ctx.Param("artifactID")
	out, replay, err := c.service.Create(ctx, &req)
	if err != nil {
		abort(ctx, err)
		return
	}
	status := 201
	if replay {
		status = 200
	}
	ctx.JSON(status, out)
}

// List godoc
// @Summary List draft summaries by ascending ID
// @Tags drafts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Param cursor query string false "Query-bound cursor"
// @Param limit query int false "Page size" default(50) minimum(1) maximum(200)
// @Produce json
// @Success 200 {object} artifactv1.ListDraftsResponse
// @Failure 400,401,404,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/drafts [get]
func (c *Controller) List(ctx *core.Context) {
	var req api.ListDraftsRequest
	if err := defaults.Set(&req); err != nil {
		abort(ctx, serrors.ErrorInternalError)
		return
	}
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		abort(ctx, err)
		return
	}
	out, err := c.service.List(ctx, &req)
	if err != nil {
		abort(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Get godoc
// @Summary Read one consistent draft snapshot
// @Tags drafts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Param draftID path string true "Draft ID"
// @Produce json
// @Success 200 {object} artifactv1.DraftResponse
// @Failure 400,401,404,500 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/drafts/{draftID} [get]
func (c *Controller) Get(ctx *core.Context) {
	var req api.GetDraftRequest
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		abort(ctx, err)
		return
	}
	out, err := c.service.Get(ctx, &req)
	if err != nil {
		abort(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Update godoc
// @Summary Replace a draft snapshot with revision checking and replay receipt
// @Tags drafts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Param draftID path string true "Draft ID"
// @Accept json
// @Produce json
// @Param request body artifactv1.UpdateDraftRequest true "Complete snapshot and expected revision"
// @Success 200 {object} artifactv1.UpdateDraftResponse
// @Failure 400,401,403,404,409,413,500,503 {object} artifactv1.ErrorResponse
// @Failure 410 {object} artifactv1.DraftDeletedResponse
// @Router /v1/artifacts/{artifactID}/drafts/{draftID} [put]
func (c *Controller) Update(ctx *core.Context) {
	var req api.UpdateDraftRequest
	if !c.decode(ctx, &req, true) {
		return
	}
	req.ArtifactID = ctx.Param("artifactID")
	req.DraftID = ctx.Param("draftID")
	out, err := c.service.Update(ctx, &req)
	if err != nil {
		abort(ctx, err)
		return
	}
	ctx.JSON(200, out)
}

// Delete godoc
// @Summary Delete a draft while retaining successful request identities
// @Tags drafts
// @Security BearerAuth
// @Param X-Tenant-ID header string true "Tenant scope selection"
// @Param X-Project-ID header string false "Project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param artifactID path string true "Artifact ID"
// @Param draftID path string true "Draft ID"
// @Param expectedRevision query int true "Current draft revision" minimum(1)
// @Success 204
// @Failure 400,401,404,409,500,503 {object} artifactv1.ErrorResponse
// @Router /v1/artifacts/{artifactID}/drafts/{draftID} [delete]
func (c *Controller) Delete(ctx *core.Context) {
	var req api.DeleteDraftRequest
	if err := binder.Bind(ctx, &req).All().Error(); err != nil {
		abort(ctx, err)
		return
	}
	if err := c.service.Delete(ctx, &req); err != nil {
		abort(ctx, err)
		return
	}
	ctx.Status(204)
}
