package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestResolveCollectionVersionAliasLatestAndFiles(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		storage := &publicationStorage{expiry: time.Now().UTC().Add(time.Hour), refs: map[string]string{}}
		client, _, _ := publicationHTTP(t, database, storage)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "resolve", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		refs := []api.FileReference{
			{FileID: "file-A", Path: "目录/part #1%.json"},
			{FileID: "file-B", Path: "目录/part #1%.JSON"},
			{FileID: "file-C", Path: "folder/name "},
		}
		first, err := client.CreateVersion(
			ctx,
			&api.CreateVersionRequest{
				ArtifactPath: ap,
				RequestKey:   "first",
				Payload:      json.RawMessage(`{"n":1}`),
				Files:        refs,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		second, err := client.CreateVersion(
			ctx,
			&api.CreateVersionRequest{
				ArtifactPath: ap,
				RequestKey:   "second",
				Payload:      json.RawMessage(`{"n":2}`),
				Files:        refs,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		aliasPath := api.AliasPath{ArtifactPath: ap, Name: "production"}
		alias, err := client.PutAlias(
			ctx,
			&api.PutAliasRequest{AliasPath: aliasPath, VersionID: first.ID, ExpectedRevision: revision(0)},
		)
		if err != nil {
			t.Fatal(err)
		}
		beforeMetadata, beforeBind, beforeRelease := storage.metadataCalls.Load(), storage.bindCalls.Load(), storage.releaseCalls.Load()
		var changesBefore, tasksBefore int64
		if err := database.Model(&table.ReferenceChange{}).Count(&changesBefore).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Model(&table.BackgroundTask{}).Count(&tasksBefore).Error; err != nil {
			t.Fatal(err)
		}
		collection, err := client.Resolve(ctx, &api.ResolveRequest{URI: a.ArtifactURI})
		if err != nil || collection.Artifact.ID != a.ID || collection.Version != nil || collection.File != nil {
			t.Fatalf("collection %+v %v", collection, err)
		}
		for _, tc := range []struct {
			uri     string
			version *api.VersionResponse
			fileID  string
		}{
			{uri: first.VersionURI, version: first, fileID: ""},
			{uri: a.ArtifactURI + "/aliases/production", version: first, fileID: ""},
			{uri: a.ArtifactURI + "/aliases/latest", version: second, fileID: ""},
			{uri: first.Files[0].FileURI, version: first, fileID: first.Files[0].FileID},
			{uri: first.VersionURI + "/files/folder/name%20", version: first, fileID: "file-C"},
			{uri: a.ArtifactURI + "/aliases/production/files/%E7%9B%AE%E5%BD%95/part%20%231%25.json", version: first, fileID: "file-A"},
			{uri: a.ArtifactURI + "/aliases/latest/files/%E7%9B%AE%E5%BD%95/part%20%231%25.JSON", version: second, fileID: "file-B"},
		} {
			result, err := client.Resolve(ctx, &api.ResolveRequest{URI: tc.uri})
			if err != nil || result.Version == nil || result.Version.ID != tc.version.ID ||
				result.Version.VersionURI != tc.version.VersionURI ||
				result.Version.Version != tc.version.Version ||
				string(result.Version.Payload) != string(tc.version.Payload) {
				t.Fatalf("resolve %s: %+v %v", tc.uri, result, err)
			}
			if tc.fileID == "" {
				if result.File != nil {
					t.Fatalf("unexpected file %+v", result.File)
				}
			} else {
				if result.File == nil || result.File.FileID != tc.fileID ||
					!strings.HasPrefix(result.File.FileURI, tc.version.VersionURI+"/files/") {
					t.Fatalf("file identity %+v", result.File)
				}
			}
		}
		if storage.metadataCalls.Load() != beforeMetadata || storage.bindCalls.Load() != beforeBind ||
			storage.releaseCalls.Load() != beforeRelease {
			t.Fatal("Resolve called storage")
		}
		var changesAfter, tasksAfter int64
		if err := database.Model(&table.ReferenceChange{}).Count(&changesAfter).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Model(&table.BackgroundTask{}).Count(&tasksAfter).Error; err != nil {
			t.Fatal(err)
		}
		if changesAfter != changesBefore || tasksAfter != tasksBefore {
			t.Fatal("Resolve created retention/reference obligations")
		}
		_, err = client.PutAlias(
			ctx,
			&api.PutAliasRequest{
				AliasPath:        aliasPath,
				VersionID:        second.ID,
				ExpectedRevision: &alias.Revision,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		moved, err := client.Resolve(ctx, &api.ResolveRequest{URI: a.ArtifactURI + "/aliases/production"})
		if err != nil || moved.Version.ID != second.ID {
			t.Fatalf("moved alias %+v %v", moved, err)
		}
		fixed, err := client.Resolve(ctx, &api.ResolveRequest{URI: first.VersionURI})
		if err != nil || fixed.Version.ID != first.ID {
			t.Fatalf("fixed version moved %+v %v", fixed, err)
		}
		if _, err := client.DeleteVersion(
			ctx,
			&api.DeleteVersionRequest{VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: first.ID}},
		); err != nil {
			t.Fatal(err)
		}
		for _, uri := range []string{first.VersionURI, first.Files[0].FileURI} {
			_, err := client.Resolve(ctx, &api.ResolveRequest{URI: uri})
			expectError(t, err, 404, "ResourceNotFound")
		}
		live, err := client.Resolve(ctx, &api.ResolveRequest{URI: second.Files[0].FileURI})
		if err != nil || live.File == nil {
			t.Fatalf("shared file lost business visibility %+v %v", live, err)
		}
	})
}

func TestResolveScopeTypeAndVisibility(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		// No storage is configured: Resolve is an artifact metadata operation.
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "scope-resolve", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		v, err := client.CreateVersion(ctx, &api.CreateVersionRequest{ArtifactPath: ap, RequestKey: "version"})
		if err != nil {
			t.Fatal(err)
		}
		project := create(t, client, identity(t, "project"), "scope-resolve", "model")
		for _, tc := range []struct{ token, uri string }{
			{token: "project", uri: a.ArtifactURI},
			{token: "other-tenant", uri: a.ArtifactURI},
			{token: "upper-tenant", uri: a.ArtifactURI},
			{token: "tenant", uri: project.ArtifactURI},
			{token: "other-project", uri: project.ArtifactURI},
			{token: "tenant", uri: strings.Replace(a.ArtifactURI, "/model/", "/dataset/", 1)},
			{token: "tenant", uri: v.VersionURI + "/files/missing"},
		} {
			_, err := client.Resolve(identity(t, tc.token), &api.ResolveRequest{URI: tc.uri})
			expectError(t, err, 404, "ResourceNotFound")
		}
		got, err := client.Resolve(identity(t, "project"), &api.ResolveRequest{URI: project.ArtifactURI})
		if err != nil || got.Artifact.ID != project.ID {
			t.Fatalf("project %+v %v", got, err)
		}
		aliasPath := api.AliasPath{ArtifactPath: ap, Name: "production"}
		alias, err := client.PutAlias(
			ctx,
			&api.PutAliasRequest{AliasPath: aliasPath, VersionID: v.ID, ExpectedRevision: revision(0)},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.DeleteAlias(
			ctx,
			&api.DeleteAliasRequest{AliasPath: aliasPath, ExpectedRevision: &alias.Revision},
		); err != nil {
			t.Fatal(err)
		}
		_, err = client.Resolve(ctx, &api.ResolveRequest{URI: a.ArtifactURI + "/aliases/production"})
		expectError(t, err, 404, "ResourceNotFound")
		if _, err := client.ArchiveArtifact(ctx, &api.ArchiveArtifactRequest{ArtifactPath: ap}); err != nil {
			t.Fatal(err)
		}
		for _, uri := range []string{a.ArtifactURI, v.VersionURI, a.ArtifactURI + "/aliases/latest"} {
			result, err := client.Resolve(identity(t, "historical"), &api.ResolveRequest{URI: uri})
			if err != nil || result.Artifact.ID != a.ID {
				t.Fatalf("historical archived %+v %v", result, err)
			}
		}
		if _, err := client.DeleteVersion(
			ctx,
			&api.DeleteVersionRequest{VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: v.ID}},
		); err != nil {
			t.Fatal(err)
		}
		for _, uri := range []string{v.VersionURI, a.ArtifactURI + "/aliases/latest"} {
			_, err := client.Resolve(ctx, &api.ResolveRequest{URI: uri})
			expectError(t, err, 404, "ResourceNotFound")
		}
		if result, err := client.Resolve(
			ctx,
			&api.ResolveRequest{URI: a.ArtifactURI},
		); err != nil ||
			result.Version != nil {
			t.Fatalf("empty archived collection %+v %v", result, err)
		}
	})
}

func TestResolveRejectsInvalidHTTPBodiesAndURIs(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		for _, body := range []string{`{}`, `null`, `[]`, `{"uri":null}`, `{"uri":42}`, `{"uri":""}`, `{"URI":"artifact://tenant/artifacts/model/a"}`, `{"uri":"artifact://tenant/artifacts/model/a","extra":true}`, `{"uri":"https://tenant/artifacts/model/a"}`, `{"uri":"artifact://tenant/artifacts/model/a?x=1"}`, `{"uri":"artifact://tenant/artifacts/model/a/versions/v/files/%2E%2E/x"}`} {
			draftWrite(t, http.MethodPost, server.URL+"/v1/resolve", body, 400)
		}
		body := fmt.Sprintf(`{"uri":%q}`, "artifact://"+tenantA+"/artifacts/model/absent")
		draftWrite(t, http.MethodPost, server.URL+"/v1/resolve", body, 404)
	})
}
