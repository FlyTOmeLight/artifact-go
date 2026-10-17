package artifact

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/artifact-go/internal/app/retention"
	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/cursor"
	"github.com/FlyTOmeLight/artifact-go/internal/reference"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type Service struct{ database *db.DB }

func NewService(database *db.DB) *Service { return &Service{database: database} }

var (
	typePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,127}$`)
)

func invalid(message string) error {
	return serrors.NewError(http.StatusBadRequest, api.CodeInvalidParameter, message)
}

func (s *Service) Create(c *core.Context, req *api.CreateArtifactRequest) (*api.ArtifactResponse, bool, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, false, err
	}
	if req.ProjectID != nil && (identity.ProjectID == nil || *req.ProjectID != *identity.ProjectID) {
		return nil, false, serrors.NewError(
			403,
			api.CodeScopeMismatch,
			"projectID does not match trusted scope",
		)
	}
	name := strings.ToLower(req.Name)
	if !typePattern.MatchString(req.Type) || !namePattern.MatchString(name) {
		return nil, false, invalid("type and name must be ASCII identifiers starting with a letter")
	}
	if utf8.RuneCountInString(req.RequestKey) > 64 || strings.ContainsAny(req.RequestKey, "\x00\r\n") {
		return nil, false, invalid("requestKey must not exceed 64 characters or contain NUL, CR or LF")
	}
	initial, err := version.InitialVersion(req.VersionScheme, req.InitialVersion)
	if err != nil {
		return nil, false, invalid(err.Error())
	}
	// Immutable collection fields are the successful request's canonical parameters.
	lookup := func(ctx context.Context) (*api.ArtifactResponse, error) {
		var result api.ArtifactResponse
		err := db.Transaction(ctx, s.database, db.TransactionOptions{
			Isolation: sql.LevelRepeatableRead, ReadOnly: true,
		}, func(tx *db.DB) error {
			var row table.Artifact
			if err := scoped(
				tx,
				identity,
			).Where("created_by = ? AND request_key = ?", identity.UserID, req.RequestKey).
				First(&row).
				Error; err != nil {
				return err
			}
			if row.Type != req.Type || row.Name != name || row.VersionScheme != string(req.VersionScheme) ||
				row.InitialVersion != initial {
				return serrors.NewError(
					409,
					"IdempotentParameterMismatch",
					"requestKey was used with different parameters",
				)
			}
			policy, err := retention.Load(tx, row.ID)
			if err != nil {
				return err
			}
			result = Response(row)
			result.RetentionPolicy = retention.Response(policy)
			return nil
		})
		return &result, err
	}
	if req.RequestKey != "" {
		result, err := lookup(c.Request.Context())
		if err == nil {
			return result, false, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, databaseError(err)
		}
	}
	if err = identity.RequireActive(); err != nil {
		return nil, false, err
	}
	row := table.Artifact{
		ID:        uuid.NewString(),
		TenantID:  identity.TenantID,
		ProjectID: identity.ProjectID,
		UserID:    identity.ResourceUserID,
		ScopeKey:  identity.Key(),
		Type:      req.Type,
		Name:      name,
		VersionScheme: string(
			req.VersionScheme,
		),
		InitialVersion: initial,
		State:          "active",
		CreatedBy:      identity.UserID,
	}
	if req.RequestKey != "" {
		key := req.RequestKey
		row.RequestKey = &key
	}
	completed := false
	err = db.Transaction(c.Request.Context(), s.database, db.TransactionOptions{
		Isolation: sql.LevelReadCommitted,
		Retry:     &db.TransactionRetry{MaxAttempts: 3, Timeout: 5 * time.Second, Delay: 10 * time.Millisecond},
	}, func(tx *db.DB) error {
		completed = false
		err := tx.Create(&row).Error
		completed = err == nil
		return err
	})
	if err != nil {
		if req.RequestKey != "" {
			// Unique conflicts and ambiguous COMMITs are resolved after leaving the failed transaction.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
			defer cancel()
			result, lookupErr := lookup(ctx)
			if lookupErr == nil {
				return result, false, nil
			}
			var app *serrors.Error
			if errors.As(lookupErr, &app) {
				return nil, false, lookupErr
			}
			if completed || !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return nil, false, serrors.NewError(
					503,
					"OperationOutcomeUnknown",
					"creation outcome cannot be confirmed; retry the same requestKey",
				)
			}
		} else if completed {
			return nil, false, serrors.NewError(
				503,
				"OperationOutcomeUnknown",
				"creation outcome cannot be confirmed; no idempotent retry is available without requestKey",
			)
		}
		return nil, false, databaseError(err)
	}
	result := Response(row)
	return &result, true, nil
}

func scoped(database *db.DB, identity scope.Identity) *db.DB {
	return database.Where("tenant_id = ? AND scope_key = ?", identity.TenantID, identity.Key())
}

func (s *Service) List(c *core.Context, req *api.ListArtifactsRequest) (*api.ListArtifactsResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	if req.Type != "" && !typePattern.MatchString(req.Type) {
		return nil, invalid("invalid type")
	}
	query := cursor.Query{
		TenantID: identity.TenantID,
		ScopeKey: identity.Key(),
		Type:     req.Type,
		State:    string(req.State),
	}
	after, err := cursor.Decode(req.Cursor, query)
	if err != nil {
		return nil, serrors.NewError(400, api.CodeInvalidCursor, err.Error())
	}
	q := scoped(s.database.WithContext(c.Request.Context()), identity)
	if req.Type != "" {
		q = q.Where("type = ?", req.Type)
	}
	if req.State != "" {
		q = q.Where("state = ?", req.State)
	}
	if after != "" {
		q = q.Where("id > ?", after)
	}
	var rows []table.Artifact
	if err = q.Order("id ASC").Limit(req.Limit + 1).Find(&rows).Error; err != nil {
		return nil, databaseError(err)
	}
	out := api.ListArtifactsResponse{Items: make([]api.ArtifactResponse, 0, len(rows))}
	if len(rows) > req.Limit {
		rows = rows[:req.Limit]
		out.NextCursor = cursor.Encode(query, rows[len(rows)-1].ID)
	}
	var policies []table.RetentionPolicy
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if len(ids) > 0 {
		if err = s.database.WithContext(c.Request.Context()).
			Where("artifact_id IN ?", ids).
			Find(&policies).
			Error; err != nil {
			return nil, databaseError(err)
		}
	}
	byID := make(map[string]table.RetentionPolicy, len(policies))
	for _, policy := range policies {
		byID[policy.ArtifactID] = policy
	}
	for _, row := range rows {
		result := Response(row)
		if policy, ok := byID[row.ID]; ok {
			result.RetentionPolicy = retention.Response(policy)
		}
		out.Items = append(out.Items, result)
	}
	return &out, nil
}

func (s *Service) Get(c *core.Context, id string) (*api.ArtifactResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	var row table.Artifact
	if err = scoped(
		s.database.WithContext(c.Request.Context()),
		identity,
	).First(&row, "id = ?", id).
		Error; err != nil {
		return nil, databaseError(err)
	}
	policy, err := retention.Load(s.database.WithContext(c.Request.Context()), row.ID)
	if err != nil {
		return nil, databaseError(err)
	}
	result := Response(row)
	result.RetentionPolicy = retention.Response(policy)
	return &result, nil
}

func (s *Service) Archive(c *core.Context, id string) (*api.ArtifactResponse, error) {
	identity, err := scope.From(c)
	if err != nil {
		return nil, err
	}
	var row table.Artifact
	var policy table.RetentionPolicy
	err = db.Transaction(c.Request.Context(), s.database, db.TransactionOptions{
		Isolation: sql.LevelReadCommitted,
		Retry:     &db.TransactionRetry{MaxAttempts: 3, Timeout: 5 * time.Second, Delay: 10 * time.Millisecond},
	}, func(tx *db.DB) error {
		started := time.Now()
		lockErr := scoped(
			tx,
			identity,
		).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&row, "id = ?", id).
			Error
		telemetry.ObserveArtifactLock("artifact-archive", time.Since(started))
		if lockErr != nil {
			return lockErr
		}
		var err error
		policy, err = retention.Load(tx, row.ID)
		if err != nil {
			return err
		}
		if row.State == "archived" {
			return nil
		}
		if err := tx.Model(&row).Update("state", "archived").Error; err != nil {
			return err
		}
		row.State = "archived"
		return nil
	})
	if err != nil {
		return nil, databaseError(err)
	}
	result := Response(row)
	result.RetentionPolicy = retention.Response(policy)
	return &result, nil
}

func databaseError(err error) error {
	var app *serrors.Error
	if errors.As(err, &app) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return serrors.NewError(404, api.CodeArtifactNotFound, "artifact not found")
	}
	var pgErr *pgconn.PgError
	var myErr *mysql.MySQLError
	if errors.Is(err, gorm.ErrDuplicatedKey) || (errors.As(err, &pgErr) && pgErr.Code == "23505") ||
		(errors.As(err, &myErr) && myErr.Number == 1062) {
		return serrors.NewError(
			409,
			api.CodeArtifactNameConflict,
			"artifact name already exists in this scope and type",
		)
	}
	return serrors.ErrorInternalError
}

// Response maps persisted business metadata to its public representation.
func Response(row table.Artifact) api.ArtifactResponse {
	return api.ArtifactResponse{
		ID:             row.ID,
		TenantID:       row.TenantID,
		ProjectID:      row.ProjectID,
		UserID:         row.UserID,
		Type:           row.Type,
		Name:           row.Name,
		ArtifactURI:    reference.ArtifactURI(row.TenantID, row.ProjectID, row.Type, row.ID),
		VersionScheme:  api.VersionScheme(row.VersionScheme),
		InitialVersion: row.InitialVersion,
		State:          api.ArtifactState(row.State),
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt.UTC(),
		UpdatedAt:      row.UpdatedAt.UTC(),
		RetentionPolicy: api.RetentionResponse{
			ArtifactID: row.ID,
			RetentionRules: api.RetentionRules{
				MinVersions:    1,
				OverflowAction: api.OverflowAction("reject"),
				ProtectAliases: true,
			},
		},
	}
}
