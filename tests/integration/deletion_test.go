package integration_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestDeleteVersionLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "deletion", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		req := &api.CreateVersionRequest{
			ArtifactPath: ap,
			RequestKey:   "original",
			Payload:      json.RawMessage(`{"secret":"data"}`),
			Annotations:  json.RawMessage(`{"note":"data"}`),
			Protected:    true,
		}
		v, err := c.CreateVersion(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		vp := api.VersionPath{ArtifactPath: ap, VersionID: v.ID}
		del := &api.DeleteVersionRequest{VersionPath: vp}
		_, err = c.DeleteVersion(identity(t, "project"), del)
		expectError(t, err, 404, "")
		_, err = c.DeleteVersion(ctx, del)
		expectError(t, err, 409, api.CodeVersionProtected)
		_, err = c.UpdateProtection(
			ctx,
			&api.UpdateProtectionRequest{
				VersionPath:      vp,
				ExpectedRevision: revision(1),
				Protected:        flag(false),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		alias := aliasPath(a.ID, "production")
		_, err = c.PutAlias(
			ctx,
			&api.PutAliasRequest{AliasPath: alias, VersionID: v.ID, ExpectedRevision: revision(0)},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.DeleteVersion(ctx, del)
		expectError(t, err, 409, api.CodeVersionReferenced)
		_, err = c.DeleteAlias(ctx, &api.DeleteAliasRequest{AliasPath: alias, ExpectedRevision: revision(1)})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.ArchiveArtifact(ctx, &api.ArchiveArtifactRequest{ArtifactPath: ap})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			result, e := c.DeleteVersion(identity(t, "historical"), del)
			if e != nil || result.ID != v.ID || result.State != api.VersionDeleted {
				t.Fatalf("delete %+v %v", result, e)
			}
		}
		_, err = c.GetVersion(ctx, &api.GetVersionRequest{VersionPath: vp})
		expectError(t, err, 404, "")
		listed, err := c.ListVersions(ctx, &api.ListVersionsRequest{ArtifactPath: ap})
		if err != nil || len(listed.Items) != 0 {
			t.Fatalf("listed %+v %v", listed, err)
		}
		_, err = c.CreateVersion(ctx, req)
		var gone *api.VersionDeletedError
		if !errors.As(err, &gone) || gone.Version.ID != v.ID {
			t.Fatalf("replay %v", err)
		}
		expectError(t, err, 410, api.CodeVersionDeleted)
		var tomb struct {
			Payload     []byte
			Annotations []byte
			State       string
		}
		if e := database.Table("versions").
			Select("payload, annotations, state").
			Where("id = ?", v.ID).
			Take(&tomb).
			Error; e != nil {
			t.Fatal(e)
		}
		if string(tomb.Payload) != "{}" || string(tomb.Annotations) != "{}" || tomb.State != "deleted" {
			t.Fatalf("content not cleared %+v", tomb)
		}
	})
}
