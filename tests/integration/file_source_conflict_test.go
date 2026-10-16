package integration_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	versionapp "github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestDraftPublicationSourceConflictDecision(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "source-conflict", "model")
		path := api.ArtifactPath{ArtifactID: a.ID}
		d, err := client.CreateDraft(ctx, &api.CreateDraftRequest{ArtifactPath: path, RequestKey: "draft"})
		if err != nil {
			t.Fatal(err)
		}
		conn, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		defer func() { database.Statement.ConnPool = original }()
		for _, mode := range []string{"normal", "commit", "rollback"} {
			req := &api.CreateVersionRequest{
				ArtifactPath:          path,
				RequestKey:            mode,
				DraftID:               &d.ID,
				ExpectedDraftRevision: revision(2),
			}
			if mode != "normal" {
				database.Statement.ConnPool = &uncertainCommitPool{
					ConnPool:   conn,
					connection: conn,
					persist:    mode == "commit",
				}
			}
			_, err = client.CreateVersion(ctx, req)
			database.Statement.ConnPool = original
			if mode == "rollback" {
				expectError(t, err, 503, "OperationOutcomeUnknown")
			} else {
				expectError(t, err, 409, "RevisionConflict")
			}
			_, err = client.CreateVersion(ctx, req)
			expectError(t, err, 409, "RevisionConflict")
			var row table.ReferenceChange
			if err = database.Where("artifact_id = ? AND operation_kind = ? AND request_key = ?", a.ID, "create-version", mode).
				First(&row).
				Error; err != nil {
				t.Fatal(err)
			}
			if row.State != "aborted" || row.RequestHash == "" {
				t.Fatalf("missing aborted fingerprint: %+v", row)
			}
			req.ExpectedDraftRevision = revision(1)
			_, err = client.CreateVersion(ctx, req)
			expectError(t, err, 409, "IdempotentParameterMismatch")
			// An expired previous Bind plan must be retained before the source rejection replaces it.
			expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			plan := versionapp.ReferencePlan{
				Scope: storage.Scope{TenantID: tenantA},
				References: []versionapp.PlannedReference{
					{
						Reference: storage.Reference{
							ReferenceID: fmt.Sprintf("old-%s", mode),
							FileID:      "file-one",
						},
						OriginalExpiresAt: expiry,
					},
				},
			}
			raw, e := json.Marshal(plan)
			if e != nil {
				t.Fatal(e)
			}
			if e = database.Model(&row).
				Updates(map[string]any{"state": "preparing", "deadline": time.Now().Add(-time.Minute), "plan": raw}).
				Error; e != nil {
				t.Fatal(e)
			}
			oldAttempt := row.AttemptID
			req.ExpectedDraftRevision = revision(2)
			_, err = client.CreateVersion(ctx, req)
			expectError(t, err, 409, "RevisionConflict")
			var task table.BackgroundTask
			if e = database.Where("kind = ? AND target_id = ?", "reference-release", oldAttempt).
				First(&task).
				Error; e != nil {
				t.Fatal(e)
			}
			if task.NotBefore.Before(expiry) {
				t.Fatal("source conflict shortened original preparation window")
			}
			var saved versionapp.ReferencePlan
			if e = json.Unmarshal(
				task.Payload,
				&saved,
			); e != nil || len(saved.References) != 1 ||
				saved.References[0].ReferenceID != fmt.Sprintf("old-%s", mode) {
				t.Fatalf("lost old compensation: %s %v", task.Payload, e)
			}
		}
	})
}
