package retention

import (
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/reference"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type Service struct {
	database            *db.DB
	maxRequestBodyBytes int64
}

func NewService(database *db.DB, maxRequestBodyBytes int64) *Service {
	return &Service{database: database, maxRequestBodyBytes: maxRequestBodyBytes}
}

func invalid(message string) error { return serrors.NewError(400, "InvalidRetentionRequest", message) }

func databaseError(err error) error {
	var e *serrors.Error
	if errors.As(err, &e) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return serrors.NewError(404, "ResourceNotFound", "resource not found")
	}
	return serrors.ErrorInternalError
}

// Load reads the policy while the caller holds the Artifact lock.
func Load(tx *db.DB, artifactID string) (table.RetentionPolicy, error) {
	p := table.RetentionPolicy{
		ArtifactID:     artifactID,
		MinVersions:    1,
		ProtectAliases: true,
		OverflowAction: "reject",
		LastResult:     []byte("{}"),
	}
	var row table.RetentionPolicy
	err := tx.Where("artifact_id = ?", artifactID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, nil
	}
	return row, err
}

// LoadFacts reads ready versions and ordinary alias targets under the caller's Artifact lock.
func LoadFacts(tx *db.DB, artifactID string) ([]VersionFact, error) {
	var rows []table.Version
	if err := tx.Select("id", "number", "sequence", "published_at", "protected").
		Where("artifact_id = ? AND state = ?", artifactID, "ready").
		Order("sequence ASC").
		Find(&rows).
		Error; err != nil {
		return nil, err
	}
	var aliases []table.Alias
	if err := tx.Select("version_id").
		Where("artifact_id = ? AND version_id IS NOT NULL", artifactID).
		Find(&aliases).
		Error; err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	for _, a := range aliases {
		targets[*a.VersionID] = true
	}
	out := make([]VersionFact, 0, len(rows))
	for _, v := range rows {
		out = append(
			out,
			VersionFact{
				ID:          v.ID,
				Number:      v.Number,
				Sequence:    v.Sequence,
				PublishedAt: v.PublishedAt,
				Protected:   v.Protected,
				Aliased:     targets[v.ID],
			},
		)
	}
	return out, nil
}

func Response(p table.RetentionPolicy) api.RetentionResponse {
	var updatedAt *time.Time
	if p.Revision > 0 {
		t := p.UpdatedAt.UTC()
		updatedAt = &t
	}
	return api.RetentionResponse{
		ArtifactID: p.ArtifactID, UpdatedAt: updatedAt,
		RetentionRules: api.RetentionRules{
			Enabled:        p.Enabled,
			MaxVersions:    p.MaxVersions,
			MaxAgeSeconds:  p.MaxAgeSeconds,
			MinVersions:    p.MinVersions,
			OverflowAction: api.OverflowAction(p.OverflowAction),
			ProtectAliases: p.ProtectAliases,
		},
		Revision: p.Revision,
	}
}

func (s *Service) Update(c *core.Context, req *api.UpdateRetentionRequest) (*api.RetentionResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision == nil || *req.ExpectedRevision < 0 || req.Enabled == nil || req.MinVersions == nil ||
		*req.MinVersions < 0 ||
		req.ProtectAliases == nil {
		return nil, invalid(
			"expectedRevision, enabled, minVersions and protectAliases are required and must be valid",
		)
	}
	if (req.MaxVersions != nil && *req.MaxVersions <= 0) || (req.MaxAgeSeconds != nil && *req.MaxAgeSeconds <= 0) ||
		(req.MaxVersions != nil && *req.MinVersions > *req.MaxVersions) {
		return nil, invalid("retention limits must be positive and minVersions must not exceed maxVersions")
	}
	if req.OverflowAction != "reject" && req.OverflowAction != "deleteOldest" {
		return nil, invalid("overflowAction must be reject or deleteOldest")
	}
	if *req.Enabled && req.MaxVersions == nil && req.MaxAgeSeconds == nil {
		return nil, invalid("enabled retention requires a count or age limit")
	}
	var out api.RetentionResponse
	completed := false
	err = db.Transaction(
		c.Request.Context(),
		s.database,
		db.TransactionOptions{
			Isolation: sql.LevelReadCommitted,
			Retry: &db.TransactionRetry{
				MaxAttempts: 3,
				Timeout:     5 * time.Second,
				Delay:       10 * time.Millisecond,
			},
		},
		func(tx *db.DB) error {
			completed = false
			var a table.Artifact
			started := time.Now()
			lockErr := tx.Where("id = ? AND tenant_id = ? AND scope_key = ?", req.ArtifactID, identity.TenantID, identity.Key()).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&a).
				Error
			telemetry.ObserveArtifactLock("retention-update", time.Since(started))
			if lockErr != nil {
				return lockErr
			}
			p, err := Load(tx, a.ID)
			if err != nil {
				return err
			}
			if p.Revision != *req.ExpectedRevision {
				return serrors.NewError(409, "RevisionConflict", "retention policy revision changed")
			}
			if p.Revision == math.MaxInt64 {
				return serrors.NewError(409, "RevisionExhausted", "retention policy revision exhausted")
			}
			now, err := table.DatabaseNow(tx)
			if err != nil {
				return err
			}
			p.Enabled = *req.Enabled
			p.MaxVersions = req.MaxVersions
			p.MaxAgeSeconds = req.MaxAgeSeconds
			p.MinVersions = *req.MinVersions
			p.ProtectAliases = *req.ProtectAliases
			p.OverflowAction = string(req.OverflowAction)
			p.Revision++
			p.LastEvaluatedAt = nil
			p.LastResult = []byte("{}")
			p.UpdatedAt = now
			if err := tx.Omit("Artifact").Save(&p).Error; err != nil {
				return err
			}
			var next *time.Time
			if p.Enabled {
				next = &now
			}
			if err := tx.Model(&table.Artifact{}).
				Where("id = ?", a.ID).
				Update("next_retention_at", next).
				Error; err != nil {
				return err
			}
			out = Response(p)
			completed = true
			return nil
		},
	)
	if err != nil {
		if completed {
			return nil, serrors.NewError(
				503,
				"OperationOutcomeUnknown",
				"operation outcome is unknown; read artifact retention policy to confirm",
			)
		}
		return nil, databaseError(err)
	}
	return &out, nil
}

func (s *Service) Preview(c *core.Context, req *api.PreviewRetentionRequest) (*api.PreviewRetentionResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	var out api.PreviewRetentionResponse
	err = db.Transaction(
		c.Request.Context(),
		s.database,
		db.TransactionOptions{Isolation: sql.LevelReadCommitted},
		func(tx *db.DB) error {
			var a table.Artifact
			started := time.Now()
			lockErr := tx.Where("id = ? AND tenant_id = ? AND scope_key = ?", req.ArtifactID, identity.TenantID, identity.Key()).
				Clauses(clause.Locking{Strength: "SHARE"}).
				First(&a).
				Error
			telemetry.ObserveArtifactLock("retention-preview", time.Since(started))
			if lockErr != nil {
				return lockErr
			}
			p, err := Load(tx, a.ID)
			if err != nil {
				return err
			}
			facts, err := LoadFacts(tx, a.ID)
			if err != nil {
				return err
			}
			now, err := table.DatabaseNow(tx)
			if err != nil {
				return err
			}
			plan := Plan(PlanInput{Policy: p, Versions: facts, Now: now})
			identities := map[string]api.VersionIdentity{}
			for _, v := range facts {
				identities[v.ID] = api.VersionIdentity{
					ID:         v.ID,
					ArtifactID: a.ID,
					Version:    v.Number,
					Sequence:   v.Sequence,
					VersionURI: reference.VersionURI(a.TenantID, a.ProjectID, a.Type, a.ID, v.ID),
				}
			}
			convert := func(decisions []Decision) []api.RetentionDecision {
				result := make([]api.RetentionDecision, 0, len(decisions))
				for _, d := range decisions {
					reasons := append([]string{}, d.Rules...)
					reasons = append(reasons, d.ProtectionReasons...)
					result = append(
						result,
						api.RetentionDecision{
							Version: identities[d.VersionID],
							Reasons: reasons,
						},
					)
				}
				return result
			}
			out = api.PreviewRetentionResponse{
				PolicyRevision:  p.Revision,
				CalculatedAt:    now,
				Candidates:      convert(plan.Candidates),
				Retained:        convert(plan.Retained),
				Blocked:         plan.Blocked,
				BlockingReasons: plan.BlockingReasons,
			}
			return nil
		},
	)
	if err != nil {
		return nil, databaseError(err)
	}
	return &out, nil
}
