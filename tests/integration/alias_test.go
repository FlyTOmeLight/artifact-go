package integration_test

import (
	"sync"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func revision(v int64) *int64 { return &v }

func flag(v bool) *bool { return &v }

func publishVersion(t *testing.T, c *api.Client, a string, key string) *api.VersionResponse {
	t.Helper()
	v, e := c.CreateVersion(
		identity(t, "tenant"),
		&api.CreateVersionRequest{ArtifactPath: api.ArtifactPath{ArtifactID: a}, RequestKey: key},
	)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func aliasPath(a, n string) api.AliasPath {
	return api.AliasPath{ArtifactPath: api.ArtifactPath{ArtifactID: a}, Name: n}
}

func TestAliasLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "alias-lifecycle", "model")
		v1 := publishVersion(t, c, a.ID, "one")
		v2 := publishVersion(t, c, a.ID, "two")
		p := aliasPath(a.ID, "production")
		first, err := c.PutAlias(
			ctx,
			&api.PutAliasRequest{AliasPath: p, VersionID: v1.ID, ExpectedRevision: revision(0)},
		)
		if err != nil || first.Revision != 1 || first.Target.ID != v1.ID {
			t.Fatalf("create alias: %+v %v", first, err)
		}
		latest, err := c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: aliasPath(a.ID, "latest")})
		if err != nil || latest.Revision != 0 || latest.Target.ID != v2.ID || latest.DeletedAt != nil {
			t.Fatalf("latest: %+v %v", latest, err)
		}
		_, err = c.PutAlias(
			ctx,
			&api.PutAliasRequest{
				AliasPath:        aliasPath(a.ID, "latest"),
				VersionID:        v1.ID,
				ExpectedRevision: revision(0),
			},
		)
		expectError(t, err, 400, "")
		_, err = c.DeleteAlias(
			ctx,
			&api.DeleteAliasRequest{AliasPath: aliasPath(a.ID, "latest"), ExpectedRevision: revision(0)},
		)
		expectError(t, err, 400, "")
		for _, token := range []string{"project", "other-tenant"} {
			_, err = c.GetAlias(identity(t, token), &api.GetAliasRequest{AliasPath: p})
			expectError(t, err, 404, "")
		}
		other := create(t, c, ctx, "alias-other", "model")
		ov := publishVersion(t, c, other.ID, "one")
		_, err = c.PutAlias(
			ctx,
			&api.PutAliasRequest{AliasPath: p, VersionID: ov.ID, ExpectedRevision: revision(1)},
		)
		expectError(t, err, 404, "")
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for _, id := range []string{v1.ID, v2.ID} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				_, e := c.PutAlias(
					ctx,
					&api.PutAliasRequest{
						AliasPath:        p,
						VersionID:        id,
						ExpectedRevision: revision(1),
					},
				)
				results <- e
			}(id)
		}
		wg.Wait()
		close(results)
		wins := 0
		for e := range results {
			if e == nil {
				wins++
			} else {
				expectError(t, e, 409, api.CodeRevisionConflict)
			}
		}
		if wins != 1 {
			t.Fatalf("CAS winners=%d", wins)
		}
		tomb, err := c.DeleteAlias(ctx, &api.DeleteAliasRequest{AliasPath: p, ExpectedRevision: revision(2)})
		if err != nil || tomb.Revision != 3 || tomb.Target != nil || tomb.DeletedAt == nil {
			t.Fatalf("delete: %+v %v", tomb, err)
		}
		_, err = c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: p})
		expectError(t, err, 404, "")
		tomb, err = c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: p, IncludeDeleted: true})
		if err != nil || tomb.Revision != 3 || tomb.Target != nil {
			t.Fatalf("tombstone: %+v %v", tomb, err)
		}
		_, err = c.PutAlias(
			ctx,
			&api.PutAliasRequest{AliasPath: p, VersionID: v2.ID, ExpectedRevision: revision(0)},
		)
		expectError(t, err, 409, api.CodeRevisionConflict)
		restored, err := c.PutAlias(
			ctx,
			&api.PutAliasRequest{AliasPath: p, VersionID: v2.ID, ExpectedRevision: revision(3)},
		)
		if err != nil || restored.Revision != 4 || restored.DeletedAt != nil {
			t.Fatalf("restore: %+v %v", restored, err)
		}
		_, err = c.DeleteAlias(ctx, &api.DeleteAliasRequest{AliasPath: p, ExpectedRevision: revision(2)})
		expectError(t, err, 409, api.CodeRevisionConflict)
		_, err = c.GetAlias(
			ctx,
			&api.GetAliasRequest{AliasPath: aliasPath(a.ID, "absent"), IncludeDeleted: true},
		)
		expectError(t, err, 404, "")
		// Simulate a later lifecycle transition without implementing T04 deletion.
		if e := database.Exec("UPDATE versions SET state = ? WHERE id = ?", "deleted", v2.ID).Error; e != nil {
			t.Fatal(e)
		}
		_, err = c.PutAlias(
			ctx,
			&api.PutAliasRequest{
				AliasPath:        aliasPath(a.ID, "hidden"),
				VersionID:        v2.ID,
				ExpectedRevision: revision(0),
			},
		)
		expectError(t, err, 404, "")
		_, err = c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: p, IncludeDeleted: true})
		expectError(t, err, 404, "")
		latest, err = c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: aliasPath(a.ID, "latest")})
		if err != nil || latest.Target.ID != v1.ID {
			t.Fatalf("latest invisible filter: %+v %v", latest, err)
		}
	})
}

func TestPublishAliasAtomicityAndReplay(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "alias-publish", "model")
		request := &api.CreateVersionRequest{
			ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
			RequestKey:   "first",
			Protected:    true,
			Aliases: []api.AliasUpdate{
				{Name: "prod", ExpectedRevision: revision(0)},
				{Name: "stable", ExpectedRevision: revision(0)},
			},
		}
		first, err := c.CreateVersion(ctx, request)
		if err != nil || !first.Protected {
			t.Fatalf("publish: %+v %v", first, err)
		}
		_, err = c.CreateVersion(
			ctx,
			&api.CreateVersionRequest{
				ArtifactPath: request.ArtifactPath,
				RequestKey:   "conflict",
				Aliases: []api.AliasUpdate{
					{Name: "new", ExpectedRevision: revision(0)},
					{Name: "prod", ExpectedRevision: revision(0)},
				},
			},
		)
		expectError(t, err, 409, api.CodeRevisionConflict)
		_, err = c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: aliasPath(a.ID, "new")})
		expectError(t, err, 404, "")
		second := publishVersion(t, c, a.ID, "second")
		if second.Sequence != 2 {
			t.Fatalf("failed publication consumed sequence: %+v", second)
		}
		_, err = c.PutAlias(
			ctx,
			&api.PutAliasRequest{
				AliasPath:        aliasPath(a.ID, "prod"),
				VersionID:        second.ID,
				ExpectedRevision: revision(1),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		vp := api.VersionPath{ArtifactPath: request.ArtifactPath, VersionID: first.ID}
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
		request.Aliases[0], request.Aliases[1] = request.Aliases[1], request.Aliases[0]
		replay, err := c.CreateVersion(ctx, request)
		if err != nil || replay.ID != first.ID || replay.Protected || replay.Revision != 2 {
			t.Fatalf("replay: %+v %v", replay, err)
		}
		alias, err := c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: aliasPath(a.ID, "prod")})
		if err != nil || alias.Target.ID != second.ID || alias.Revision != 2 {
			t.Fatalf("replay moved alias: %+v %v", alias, err)
		}
		for i, aliases := range [][]api.AliasUpdate{{{Name: "latest", ExpectedRevision: revision(0)}}, {{Name: "same", ExpectedRevision: revision(0)}, {Name: "same", ExpectedRevision: revision(0)}}, {{Name: "bad.name", ExpectedRevision: revision(0)}}} {
			_, err = c.CreateVersion(
				ctx,
				&api.CreateVersionRequest{
					ArtifactPath: request.ArtifactPath,
					RequestKey:   []string{"latest", "duplicate", "invalid"}[i],
					Aliases:      aliases,
				},
			)
			expectError(t, err, 400, "")
		}
	})
}
