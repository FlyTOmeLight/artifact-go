package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

type FileController struct{ service *Service }

func NewFileController(service *Service) *FileController { return &FileController{service: service} }

func (c *FileController) RegisterRoutes(g *core.RouterGroup) {
	g.POST("/storage/files", c.Register)
	g.POST("/storage/files/complete", c.Complete)
	g.GET("/storage/files/:fileID", c.Get)
	g.DELETE("/storage/files/:fileID", c.Delete)
}

func (c *FileController) array(ctx *core.Context) ([]json.RawMessage, error) {
	var items []json.RawMessage
	d := json.NewDecoder(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.service.limits.MaxRequestBodyBytes))
	err := d.Decode(&items)
	if err == nil {
		var extra any
		if next := d.Decode(&extra); next != io.EOF {
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
			return nil, fileError(413, "RequestBodyTooLarge", "request body exceeds configured limit")
		}
		return nil, invalidFile("expected one JSON array")
	}
	if len(items) < 1 || len(items) > 1000 {
		return nil, invalidFile("array must contain 1 to 1000 items")
	}
	return items, nil
}

func resultError(err error) *api.FileError {
	e := serrors.New(fileDBError(err))
	return &api.FileError{Code: e.Code, Message: e.Message, Status: e.Status}
}

// Register godoc
// @Summary Register a batch of files for direct upload
// @Tags storage
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param x-forge-tenant-id header string true "Trusted tenant scope selection"
// @Param x-forge-project-id header string false "Trusted project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param request body []storagev1.RegisterFileItem true "File declarations"
// @Success 200 {array} storagev1.RegisterFileResult
// @Failure 400,401,403,413 {object} errors.Error
// @Router /v1/storage/files [post]
func (c *FileController) Register(ctx *core.Context) {
	if _, err := scope.From(ctx); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	items, err := c.array(ctx)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	keys := make(map[string]bool, len(items))
	requests := make([]api.RegisterFileItem, len(items))
	errs := make([]error, len(items))
	for n, raw := range items {
		if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
			serrors.AbortWithError(ctx, invalidFile("every registration item must be an object"))
			return
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		var key string
		if value, ok := fields["requestKey"]; ok && json.Unmarshal(value, &key) == nil && key != "" {
			if keys[key] {
				serrors.AbortWithError(ctx, invalidFile("duplicate requestKey"))
				return
			}
			keys[key] = true
		}
		requests[n].RequestKey = key
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if e := d.Decode(&requests[n]); e != nil {
			errs[n] = invalidFile("invalid file declaration fields")
		}
	}
	out := make([]api.RegisterFileResult, len(items))
	for n, req := range requests {
		out[n].RequestKey = req.RequestKey
		if errs[n] != nil {
			out[n].Error = resultError(errs[n])
			continue
		}
		row, e := c.service.RegisterFile(ctx, req)
		if e != nil {
			out[n].Error = resultError(e)
		} else {
			out[n].FileResponse = row
		}
	}
	for _, item := range out {
		telemetry.FileResult("register", item.Error != nil)
	}
	ctx.JSON(200, out)
}

// Complete godoc
// @Summary Confirm uploaded objects in input order
// @Tags storage
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param x-forge-tenant-id header string true "Trusted tenant scope selection"
// @Param x-forge-project-id header string false "Trusted project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param request body []string true "File IDs"
// @Success 200 {array} storagev1.CompleteFileResult
// @Failure 400,401,403,413 {object} errors.Error
// @Router /v1/storage/files/complete [post]
func (c *FileController) Complete(ctx *core.Context) {
	if _, err := scope.From(ctx); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	items, err := c.array(ctx)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ids := make([]string, len(items))
	seen := make(map[string]bool, len(items))
	for n, raw := range items {
		if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '"' ||
			json.Unmarshal(raw, &ids[n]) != nil {
			serrors.AbortWithError(ctx, invalidFile("every confirmation item must be a string"))
			return
		}
		if seen[ids[n]] {
			serrors.AbortWithError(ctx, invalidFile("duplicate fileID"))
			return
		}
		seen[ids[n]] = true
	}
	out := make([]api.CompleteFileResult, len(items))
	for n, id := range ids {
		out[n].FileID = id
		row, e := c.service.CompleteFile(ctx, id)
		if e != nil {
			out[n].Error = resultError(e)
		} else {
			out[n].FileResponse = row
		}
	}
	for _, item := range out {
		telemetry.FileResult("complete", item.Error != nil)
	}
	ctx.JSON(200, out)
}

// Get godoc
// @Summary Read file status in the exact scope
// @Tags storage
// @Security BearerAuth
// @Produce json
// @Param x-forge-tenant-id header string true "Trusted tenant scope selection"
// @Param x-forge-project-id header string false "Trusted project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param fileID path string true "File ID"
// @Success 200 {object} storagev1.FileResponse
// @Failure 401,403,404 {object} errors.Error
// @Router /v1/storage/files/{fileID} [get]
func (c *FileController) Get(ctx *core.Context) {
	row, err := c.service.GetFile(ctx, ctx.Param("fileID"))
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, row)
}

// Delete godoc
// @Summary Request reclamation of an unreferenced file
// @Tags storage
// @Security BearerAuth
// @Produce json
// @Param x-forge-tenant-id header string true "Trusted tenant scope selection"
// @Param x-forge-project-id header string false "Trusted project scope selection"
// @Param X-Resource-User-ID header string false "Resource owner selection; omit for shared scope, verified by resolver"
// @Param fileID path string true "File ID"
// @Success 202 {object} storagev1.FileResponse
// @Success 200 {object} storagev1.FileResponse
// @Failure 401,403,404,409,503 {object} errors.Error
// @Router /v1/storage/files/{fileID} [delete]
func (c *FileController) Delete(ctx *core.Context) {
	row, status, err := c.service.DeleteFile(ctx, ctx.Param("fileID"))
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(status, row)
}
