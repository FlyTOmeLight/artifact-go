package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

type GrantController struct {
	service *Service
	signer  GrantSigner
	config  GrantConfig
	recheck GrantIdentityCheck
}

func NewGrantController(
	service *Service,
	signer GrantSigner,
	config GrantConfig,
	recheck GrantIdentityCheck,
) *GrantController {
	if config.UploadTTL == 0 {
		config.UploadTTL = time.Hour
	}
	if config.ReadTTL == 0 {
		config.ReadTTL = time.Hour
	}
	config.AllowedActions = slices.Clone(config.AllowedActions)
	return &GrantController{service: service, signer: signer, config: config, recheck: recheck}
}

func (c *GrantController) RegisterRoutes(g *core.RouterGroup) { g.POST("/storage/grants", c.Create) }

func invalidGrant(message string) error { return fileError(400, "InvalidGrantRequest", message) }

func invalidScope(message string) error { return fileError(400, "InvalidGrantScope", message) }

func (c *GrantController) request(ctx *core.Context) (api.CreateGrantRequest, error) {
	var req api.CreateGrantRequest
	var raw json.RawMessage
	decoder := json.NewDecoder(
		http.MaxBytesReader(ctx.Writer, ctx.Request.Body, c.service.limits.MaxRequestBodyBytes),
	)
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
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return req, fileError(413, "RequestBodyTooLarge", "request body exceeds configured limit")
		}
		return req, invalidGrant("expected one JSON object")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return req, invalidGrant("expected a JSON object")
	}
	for key := range fields {
		if key != "fileIDs" && key != "scope" && key != "ttlSeconds" {
			return req, invalidGrant("unknown request field")
		}
	}
	scopeRaw, ok := fields["scope"]
	if !ok {
		return req, invalidScope("scope is required")
	}
	var scopeFields map[string]json.RawMessage
	if json.Unmarshal(scopeRaw, &scopeFields) != nil || len(scopeFields) != 1 {
		return req, invalidScope("scope must contain exactly one of preset or actions")
	}
	for key, value := range scopeFields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return req, invalidScope("scope fields cannot be null")
		}
		switch key {
		case "preset":
			if json.Unmarshal(value, &req.Scope.Preset) != nil || req.Scope.Preset == "" {
				return req, invalidScope("preset must be upload or download")
			}
		case "actions":
			if json.Unmarshal(value, &req.Scope.Actions) != nil || len(req.Scope.Actions) == 0 {
				return req, invalidScope("actions must be a nonempty array")
			}
		default:
			return req, invalidScope("unknown scope field")
		}
	}
	if _, _, _, err = grantActions(req.Scope); err != nil {
		return req, err
	}
	var idValues []json.RawMessage
	if json.Unmarshal(fields["fileIDs"], &idValues) != nil || len(idValues) < 1 || len(idValues) > 1000 {
		return req, invalidGrant("fileIDs must contain 1 to 1000 unique IDs")
	}
	req.FileIDs = make([]string, len(idValues))
	seen := map[string]bool{}
	for n, value := range idValues {
		if json.Unmarshal(value, &req.FileIDs[n]) != nil || req.FileIDs[n] == "" || seen[req.FileIDs[n]] {
			return req, invalidGrant("fileIDs must contain nonempty unique strings")
		}
		seen[req.FileIDs[n]] = true
	}
	if value, ok := fields["ttlSeconds"]; ok {
		var seconds int64
		if json.Unmarshal(value, &seconds) != nil || seconds <= 0 {
			return req, invalidGrant("ttlSeconds must be a positive integer")
		}
		req.TTLSeconds = &seconds
	}
	return req, nil
}

// Create godoc
// @Summary Issue one exact-key S3 grant for a file batch
// @Tags storage
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param X-Tenant-ID header string true "Trusted tenant scope selection"
// @Param X-Project-ID header string false "Trusted project scope selection"
// @Param request body storagev1.CreateGrantRequest true "Files and requested S3 actions"
// @Success 200 {object} storagev1.GrantResponse
// @Failure 400,401,403,404,409,413,503 {object} errors.Error
// @Router /v1/storage/grants [post]
func (c *GrantController) Create(ctx *core.Context) {
	ctx.Writer.Header().Set("Cache-Control", "no-store")
	if _, err := scope.From(ctx); err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	if c.signer == nil || c.recheck == nil {
		serrors.AbortWithError(ctx, fileError(503, "GrantUnavailable", "grant issuer is not configured"))
		return
	}
	req, err := c.request(ctx)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	out, err := c.create(ctx, req)
	if err != nil {
		serrors.AbortWithError(ctx, err)
		return
	}
	ctx.JSON(200, out)
}
