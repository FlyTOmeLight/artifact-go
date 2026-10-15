package integration_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// Losing a COMMIT acknowledgement must never turn a retry into a second edit.
func TestDraftUnknownCommitOutcome(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "draft-outcomes", "model")
		path := api.ArtifactPath{ArtifactID: a.ID}
		conn, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		defer func() { database.Statement.ConnPool = original }()
		for _, persist := range []bool{true, false} {
			req := &api.CreateDraftRequest{
				ArtifactPath: path,
				RequestKey:   fmt.Sprintf("create-%t", persist),
				Payload:      json.RawMessage(`{"n":184467440737095516160001}`),
			}
			pool := &uncertainCommitPool{ConnPool: conn, connection: conn, persist: persist}
			database.Statement.ConnPool = pool
			result, err := client.CreateDraft(ctx, req)
			database.Statement.ConnPool = original
			if persist {
				if err != nil || result.Revision != 1 {
					t.Fatalf("committed create not confirmed: %+v %v", result, err)
				}
			} else {
				expectError(t, err, 503, "OperationOutcomeUnknown")
			}
			// Confirmation may use a read-only transaction; no new draft is inserted.
			again, err := client.CreateDraft(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if persist && again.ID != result.ID {
				t.Fatal("confirmation produced another draft")
			}
			dp := api.DraftPath{ArtifactPath: path, DraftID: again.ID}
			save := &api.UpdateDraftRequest{
				DraftPath:        dp,
				RequestKey:       fmt.Sprintf("save-%t", persist),
				ExpectedRevision: revision(1),
				Payload:          json.RawMessage(`{"n":1e400}`),
				Annotations:      json.RawMessage(`{}`),
				Files:            []api.FileReference{},
			}
			pool = &uncertainCommitPool{ConnPool: conn, connection: conn, persist: persist}
			database.Statement.ConnPool = pool
			saved, err := client.UpdateDraft(ctx, save)
			database.Statement.ConnPool = original
			if persist {
				if err != nil || saved.AppliedRevision != 2 || saved.Draft.Revision != 2 {
					t.Fatalf("committed save not confirmed: %+v %v", saved, err)
				}
			} else {
				expectError(t, err, 503, "OperationOutcomeUnknown")
			}
			replay, err := client.UpdateDraft(ctx, save)
			if err != nil || replay.AppliedRevision != 2 || replay.Draft.Revision != 2 ||
				string(replay.Draft.Payload) != `{"n":1e400}` {
				t.Fatalf("save retry changed outcome: %+v %v", replay, err)
			}
		}
		list, err := client.ListDrafts(ctx, &api.ListDraftsRequest{ArtifactPath: path})
		if err != nil || len(list.Items) != 2 {
			t.Fatalf("duplicate outcomes: %+v %v", list, err)
		}
	})
}
