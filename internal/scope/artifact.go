package scope

import (
	"net/http"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
)

const (
	memoryArtifactType    = "agent-memory"
	memoryArchiverService = "agentic-kit-api"
)

func (i Identity) RequireArtifactType(artifactType string) error {
	if artifactType == memoryArtifactType && i.PropagatingService != memoryArchiverService {
		return serrors.NewError(
			http.StatusForbidden,
			"ScopeAccessDenied",
			"artifact type requires a trusted service",
		)
	}
	return nil
}

func (i Identity) Artifacts(database *db.DB) *db.DB {
	query := database.Where("tenant_id = ? AND scope_key = ?", i.TenantID, i.Key())
	if i.PropagatingService != memoryArchiverService {
		query = query.Where("type <> ?", memoryArtifactType)
	}
	return query
}
