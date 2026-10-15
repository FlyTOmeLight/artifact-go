package integration_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestDraftConcurrentReadSnapshot(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		artifact := create(t, client, ctx, "draft-snapshot", "config")
		ap := api.ArtifactPath{ArtifactID: artifact.ID}
		draft, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: ap,
				RequestKey:   "create",
				Payload:      json.RawMessage(`{"revision":1}`),
				Annotations:  json.RawMessage(`{"revision":1}`),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		dp := api.DraftPath{ArtifactPath: ap, DraftID: draft.ID}
		done := make(chan error, 1)
		go func() {
			for n := int64(2); n <= 20; n++ {
				raw := json.RawMessage(fmt.Sprintf(`{"revision":%d}`, n))
				saved, e := client.UpdateDraft(
					ctx,
					&api.UpdateDraftRequest{
						DraftPath:        dp,
						RequestKey:       fmt.Sprintf("edit-%d", n),
						ExpectedRevision: revision(n - 1),
						Payload:          raw,
						Annotations:      raw,
						Files:            []api.FileReference{},
					},
				)
				if e != nil {
					done <- e
					return
				}
				if saved.AppliedRevision != n || saved.Draft.Revision != n {
					done <- fmt.Errorf("write response mixed revisions: %+v", saved)
					return
				}
			}
			done <- nil
		}()
		// Each snapshot must carry content belonging to its own revision, including
		// reads interleaved with a writer on another database connection.
		var readErr error
		for range 25 {
			got, e := client.GetDraft(ctx, &api.GetDraftRequest{DraftPath: dp})
			if e != nil {
				readErr = e
				break
			}
			want := fmt.Sprintf(`{"revision":%d}`, got.Revision)
			if string(got.Payload) != want || string(got.Annotations) != want {
				readErr = fmt.Errorf("mixed snapshot: %+v", got)
				break
			}
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
	})
}
