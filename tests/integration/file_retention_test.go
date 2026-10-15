package integration_test

import (
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestFileRetentionPreservesDraftAndNewVersionReferences(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		client, storageDB, handoff := fileDraftHTTP(t, database, nil)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "file-retention", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		file := draftReadyFile(t, storageDB)
		draft, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: ap,
				RequestKey:   "draft",
				Files:        []api.FileReference{{FileID: file, Path: "model.json"}},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.UpdateRetention(ctx, policyRequest(a.ID, 0, revision(1), api.OverflowDeleteOldest))
		if err != nil {
			t.Fatal(err)
		}
		publish := func(key string) *api.VersionResponse {
			t.Helper()
			v, err := client.CreateVersion(
				ctx,
				&api.CreateVersionRequest{
					ArtifactPath:          ap,
					RequestKey:            key,
					DraftID:               &draft.ID,
					ExpectedDraftRevision: &draft.Revision,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		old := publish("old")
		current := publish("current")
		_, err = client.GetVersion(
			ctx,
			&api.GetVersionRequest{VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: old.ID}},
		)
		expectError(t, err, 404, "")
		runReferenceTasks(t, database, handoff)
		var oldRow table.Version
		if err = database.First(&oldRow, "id = ?", old.ID).Error; err != nil || oldRow.State != "deleted" {
			t.Fatalf("retired version %+v %v", oldRow, err)
		}
		var count int64
		if err = storageDB.Model(&st.StorageReference{}).
			Where("file_id = ? AND state = ?", file, "active").
			Count(&count).
			Error; err != nil ||
			count != 2 {
			t.Fatalf("draft+new version references %d %v", count, err)
		}
		var stored st.StorageFile
		if err = storageDB.First(&stored, "id = ?", file).Error; err != nil || stored.State != "bound" {
			t.Fatalf("retention reclaimed shared file %+v %v", stored, err)
		}
		got, err := client.GetVersion(
			ctx,
			&api.GetVersionRequest{VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: current.ID}},
		)
		if err != nil || len(got.Files) != 1 || got.Files[0].FileID != file {
			t.Fatalf("current manifest %+v %v", got, err)
		}
		still, err := client.GetDraft(
			ctx,
			&api.GetDraftRequest{DraftPath: api.DraftPath{ArtifactPath: ap, DraftID: draft.ID}},
		)
		if err != nil || still.Revision != draft.Revision || len(still.Files) != 1 {
			t.Fatalf("retention changed draft %+v %v", still, err)
		}
	})
}
