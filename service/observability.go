package service

import (
	"strconv"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/FlyTOmeLight/server-go/metrics"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
)

// observeRequests runs before authentication so rejected requests are counted too.
// It records route templates and status only as labels, never credentials or IDs.
func observeRequests(domain string) core.HandlerFunc {
	return func(ctx *core.Context) {
		ctx.Next()
		route := ctx.FullPath()
		if route == "" {
			return
		}
		status := ctx.Writer.Status()
		telemetry.Request(domain, ctx.Request.Method, route, strconv.Itoa(status))
		identity, _ := scope.From(ctx)
		project := ""
		if identity.ProjectID != nil {
			project = *identity.ProjectID
		}
		artifactID, versionID := ctx.Param("artifactID"), ctx.Param("versionID")
		if value := ctx.GetString("artifact.response.artifactID"); value != "" {
			artifactID = value
		}
		if value := ctx.GetString("artifact.response.versionID"); value != "" {
			versionID = value
		}
		logger.Infof(
			ctx,
			"event=business_request domain=%s method=%s route=%q status=%d tenantID=%q projectID=%q artifactID=%q versionID=%q draftID=%q fileID=%q",
			domain,
			ctx.Request.Method,
			route,
			status,
			identity.TenantID,
			project,
			artifactID,
			versionID,
			ctx.Param("draftID"),
			ctx.Param("fileID"),
		)
	}
}

// SetupHTTP installs the opt-in framework metrics endpoint. Protect /metrics at
// the deployment ingress; it is operational and has no tenant-scoped identity.
func SetupHTTP(engine *core.Engine, config *conf.Config) error {
	if config.HTTPServer.MetricsEnable {
		engine.GET("/metrics", metrics.Metrics())
	}
	return nil
}
