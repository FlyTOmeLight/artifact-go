package integration_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func policyRequest(a string, rev int64, max *int64, action api.OverflowAction) *api.UpdateRetentionRequest {
	return &api.UpdateRetentionRequest{
		ArtifactPath:     api.ArtifactPath{ArtifactID: a},
		ExpectedRevision: revision(rev),
		Enabled:          flag(true),
		MaxVersions:      max,
		MinVersions:      revision(1),
		OverflowAction:   action,
		ProtectAliases:   flag(true),
	}
}

func TestRetentionReplacementAndPreview(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "retention-preview", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		old := publishVersion(t, c, a.ID, "one")
		latest := publishVersion(t, c, a.ID, "two")
		request := policyRequest(a.ID, 0, revision(1), api.OverflowDeleteOldest)
		policy, err := c.UpdateRetention(ctx, request)
		if err != nil || policy.Revision != 1 || policy.UpdatedAt == nil {
			t.Fatalf("policy %+v %v", policy, err)
		}
		_, err = c.UpdateRetention(ctx, request)
		expectError(t, err, 409, api.CodeRevisionConflict)
		for range 2 {
			preview, e := c.PreviewRetention(ctx, &api.PreviewRetentionRequest{ArtifactPath: ap})
			if e != nil || preview.PolicyRevision != 1 || preview.CalculatedAt.IsZero() ||
				len(preview.Candidates) != 1 ||
				preview.Candidates[0].Version.ID != old.ID ||
				len(preview.Retained) != 1 ||
				preview.Retained[0].Version.ID != latest.ID {
				t.Fatalf("preview %+v %v", preview, e)
			}
		}
		listed, err := c.ListVersions(ctx, &api.ListVersionsRequest{ArtifactPath: ap})
		if err != nil || len(listed.Items) != 2 {
			t.Fatalf("preview mutated versions %+v %v", listed, err)
		}
		// Explicit null removes both limits; false and zero are valid replacement values.
		request = policyRequest(a.ID, 1, nil, api.OverflowReject)
		request.Enabled = flag(false)
		request.MinVersions = revision(0)
		request.ProtectAliases = flag(false)
		policy, err = c.UpdateRetention(ctx, request)
		if err != nil || policy.Revision != 2 || policy.MaxVersions != nil || policy.MaxAgeSeconds != nil ||
			policy.Enabled ||
			policy.ProtectAliases ||
			policy.MinVersions != 0 {
			t.Fatalf("replacement %+v %v", policy, err)
		}
		got, err := c.GetArtifact(ctx, &api.GetArtifactRequest{ArtifactPath: ap})
		if err != nil || got.RetentionPolicy.Revision != 2 || got.RetentionPolicy.Enabled {
			t.Fatalf("artifact policy %+v %v", got, err)
		}
		root := server.URL + "/v1/artifacts/" + a.ID + "/retention-policy"
		base := `{"expectedRevision":2,"enabled":true,"maxVersions":null,"maxAgeSeconds":null,"minVersions":1,"overflowAction":"deleteOldest","protectAliases":true}`
		for _, field := range []string{`"maxVersions":null,`, `"maxAgeSeconds":null,`, `"enabled":true,`, `"minVersions":1,`, `"protectAliases":true`} {
			body := strings.Replace(base, field, "", 1)
			body = strings.Replace(body, ",}", "}", 1)
			req, e := http.NewRequestWithContext(t.Context(), http.MethodPut, root, strings.NewReader(body))
			if e != nil {
				t.Fatal(e)
			}
			req.Header.Set("Authorization", "Bearer tenant")
			req.Header.Set("Content-Type", "application/json")
			res, e := http.DefaultClient.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 400 {
				t.Fatalf("missing %s status %d body %s", field, res.StatusCode, b)
			}
		}
		_, err = c.UpdateRetention(
			identity(t, "project"),
			policyRequest(a.ID, 2, revision(1), api.OverflowReject),
		)
		expectError(t, err, 404, "")
		_, err = c.ArchiveArtifact(ctx, &api.ArchiveArtifactRequest{ArtifactPath: ap})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.UpdateRetention(
			identity(t, "historical"),
			policyRequest(a.ID, 2, revision(1), api.OverflowReject),
		)
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestRetentionPublicationAtomicity(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		for _, action := range []api.OverflowAction{api.OverflowReject, api.OverflowDeleteOldest} {
			t.Run(string(action), func(t *testing.T) {
				a := create(t, c, ctx, "retention-"+string(action), "model")
				ap := api.ArtifactPath{ArtifactID: a.ID}
				first := publishVersion(t, c, a.ID, "one")
				_, err := c.PutAlias(
					ctx,
					&api.PutAliasRequest{
						AliasPath:        aliasPath(a.ID, "production"),
						VersionID:        first.ID,
						ExpectedRevision: revision(0),
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				_, err = c.UpdateRetention(ctx, policyRequest(a.ID, 0, revision(1), action))
				if err != nil {
					t.Fatal(err)
				}
				publish := &api.CreateVersionRequest{
					ArtifactPath: ap,
					RequestKey:   "second",
					Aliases:      []api.AliasUpdate{{Name: "new", ExpectedRevision: revision(0)}},
				}
				_, err = c.CreateVersion(ctx, publish)
				expectError(t, err, 409, api.CodeRetentionBlocked)
				_, err = c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: aliasPath(a.ID, "new")})
				expectError(t, err, 404, "")
				policy := policyRequest(a.ID, 1, revision(1), api.OverflowDeleteOldest)
				policy.ProtectAliases = flag(false)
				_, err = c.UpdateRetention(ctx, policy)
				if err != nil {
					t.Fatal(err)
				}
				result, err := c.CreateVersion(ctx, publish)
				if err != nil || result.Sequence != 2 {
					t.Fatalf("publish rollback sequence %+v %v", result, err)
				}
				tomb, err := c.GetAlias(
					ctx,
					&api.GetAliasRequest{
						AliasPath:      aliasPath(a.ID, "production"),
						IncludeDeleted: true,
					},
				)
				if err != nil || tomb.Revision != 2 || tomb.Target != nil || tomb.DeletedAt == nil {
					t.Fatalf("automatic alias tombstone %+v %v", tomb, err)
				}
				_, err = c.GetVersion(
					ctx,
					&api.GetVersionRequest{
						VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: first.ID},
					},
				)
				expectError(t, err, 404, "")
				versions, err := c.ListVersions(ctx, &api.ListVersionsRequest{ArtifactPath: ap})
				if err != nil || len(versions.Items) != 1 || versions.Items[0].ID != result.ID {
					t.Fatalf("visible %+v %v", versions, err)
				}
			})
		}
	})
}

func TestRetentionPreviewAgeAndProtection(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "retention-age", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		versions := make([]*api.VersionResponse, 4)
		for i := range versions {
			versions[i] = publishVersion(t, c, a.ID, fmt.Sprint(i))
		}
		// Database fixture gives deterministic old timestamps; behavior is observed through HTTP.
		for _, v := range versions {
			if err := database.Exec(
				"UPDATE versions SET published_at = ? WHERE id = ?",
				v.PublishedAt.Add(-3600*1e9),
				v.ID,
			).Error; err != nil {
				t.Fatal(err)
			}
		}
		_, err := c.UpdateProtection(
			ctx,
			&api.UpdateProtectionRequest{
				VersionPath:      api.VersionPath{ArtifactPath: ap, VersionID: versions[0].ID},
				ExpectedRevision: revision(1),
				Protected:        flag(true),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.PutAlias(
			ctx,
			&api.PutAliasRequest{
				AliasPath:        aliasPath(a.ID, "stable"),
				VersionID:        versions[1].ID,
				ExpectedRevision: revision(0),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		policy := policyRequest(a.ID, 0, nil, api.OverflowDeleteOldest)
		policy.MaxAgeSeconds = revision(60)
		_, err = c.UpdateRetention(ctx, policy)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := c.PreviewRetention(ctx, &api.PreviewRetentionRequest{ArtifactPath: ap})
		if err != nil || len(preview.Candidates) != 1 || preview.Candidates[0].Version.ID != versions[2].ID ||
			len(preview.Retained) != 3 {
			t.Fatalf("age/protection/min %+v %v", preview, err)
		}
	})
}

func TestRetentionUsesProposedAliasTargets(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "retention-alias-move", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		first := publishVersion(t, c, a.ID, "first")
		alias := aliasPath(a.ID, "production")
		_, err := c.PutAlias(
			ctx,
			&api.PutAliasRequest{AliasPath: alias, VersionID: first.ID, ExpectedRevision: revision(0)},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.UpdateRetention(ctx, policyRequest(a.ID, 0, revision(1), api.OverflowDeleteOldest))
		if err != nil {
			t.Fatal(err)
		}
		second, err := c.CreateVersion(
			ctx,
			&api.CreateVersionRequest{
				ArtifactPath: ap,
				RequestKey:   "second",
				Aliases:      []api.AliasUpdate{{Name: "production", ExpectedRevision: revision(1)}},
			},
		)
		if err != nil {
			t.Fatalf("moving alias should release old target for retention: %v", err)
		}
		got, err := c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: alias})
		if err != nil || got.Revision != 2 || got.Target == nil || got.Target.ID != second.ID {
			t.Fatalf("moved alias %+v %v", got, err)
		}
		_, err = c.GetVersion(
			ctx,
			&api.GetVersionRequest{VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: first.ID}},
		)
		expectError(t, err, 404, "")
	})
}
