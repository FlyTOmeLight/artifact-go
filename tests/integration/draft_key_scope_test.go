package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestDraftRequestKeyScope(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		first := fixture.HTTP(t, database, resolve)
		second := fixture.HTTP(t, database, func(r *http.Request) (service.RequestIdentity, error) {
			i, e := resolve(r)
			i.UserID = "user-b"
			return i, e
		})
		aClient, bClient := versionClient(t, first.URL), versionClient(t, second.URL)
		ctx := identity(t, "tenant")
		artifact := create(t, aClient, ctx, "draft-keys", "model")
		ap := api.ArtifactPath{ArtifactID: artifact.ID}
		req := &api.CreateDraftRequest{ArtifactPath: ap, RequestKey: "shared"}
		a, e := aClient.CreateDraft(ctx, req)
		if e != nil {
			t.Fatal(e)
		}
		b, e := bClient.CreateDraft(ctx, req)
		if e != nil {
			t.Fatal(e)
		}
		if a.ID == b.ID {
			t.Fatal("different actors shared a creation receipt")
		}
		save := &api.UpdateDraftRequest{
			DraftPath:        api.DraftPath{ArtifactPath: ap, DraftID: a.ID},
			RequestKey:       "shared",
			ExpectedRevision: revision(1),
			Payload:          json.RawMessage(`{}`),
			Annotations:      json.RawMessage(`{}`),
			Files:            []api.FileReference{},
		}
		// Create and update have distinct operation namespaces.
		if _, e = aClient.UpdateDraft(ctx, save); e != nil {
			t.Fatal(e)
		}
		save.DraftID = b.ID
		if _, e = aClient.UpdateDraft(ctx, save); e == nil {
			t.Fatal("save key reused for a different draft")
		} else {
			expectError(t, e, 409, "IdempotentParameterMismatch")
		}
		if _, e = bClient.UpdateDraft(ctx, save); e != nil {
			t.Fatal(e)
		}
	})
}
