package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func draftPublishHTTP(t *testing.T, method, url, body string, status int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tenant")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != status {
		t.Fatalf("%s status=%d want=%d body=%s err=%v", method, resp.StatusCode, status, data, err)
	}
	return data
}

func TestDraftPublicationSnapshotAndReplay(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "draft-publish", "config")
		base := server.URL + "/v1/artifacts/" + a.ID + "/drafts"
		var draft struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		data := draftPublishHTTP(
			t,
			"POST",
			base,
			`{"requestKey":"draft","payload":{"n":9007199254740993},"annotations":{"note":"original"}}`,
			201,
		)
		if err := json.Unmarshal(data, &draft); err != nil || draft.ID == "" {
			t.Fatalf("draft %s %v", data, err)
		}
		req := &api.CreateVersionRequest{
			ArtifactPath:          api.ArtifactPath{ArtifactID: a.ID},
			RequestKey:            "publish",
			DraftID:               &draft.ID,
			ExpectedDraftRevision: revision(1),
		}
		first, err := c.CreateVersion(ctx, req)
		if err != nil || !strings.Contains(string(first.Payload), "9007199254740993") ||
			!strings.Contains(string(first.Annotations), "original") {
			t.Fatalf("publish %+v %v", first, err)
		}
		current := draftPublishHTTP(t, "GET", base+"/"+draft.ID, "", 200)
		if err := json.Unmarshal(current, &draft); err != nil || draft.Revision != 1 {
			t.Fatalf("publication modified draft %s", current)
		}
		req.RequestKey = "second"
		second, err := c.CreateVersion(ctx, req)
		if err != nil || second.Sequence != 2 || second.ID == first.ID {
			t.Fatalf("repeat source %+v %v", second, err)
		}
		draftPublishHTTP(
			t,
			"PUT",
			base+"/"+draft.ID,
			`{"requestKey":"save","expectedRevision":1,"payload":{"n":2},"annotations":{},"files":[]}`,
			200,
		)
		req.RequestKey = "stale"
		_, err = c.CreateVersion(ctx, req)
		expectError(t, err, 409, api.CodeRevisionConflict)
		req.RequestKey = "publish"
		replay, err := c.CreateVersion(ctx, req)
		if err != nil || replay.ID != first.ID ||
			!strings.Contains(string(replay.Payload), "9007199254740993") {
			t.Fatalf("replay edited draft %+v %v", replay, err)
		}
		draftPublishHTTP(t, "DELETE", base+"/"+draft.ID+"?expectedRevision=2", "", 204)
		replay, err = c.CreateVersion(ctx, req)
		if err != nil || replay.ID != first.ID ||
			!strings.Contains(string(replay.Payload), "9007199254740993") {
			t.Fatalf("replay deleted draft %+v %v", replay, err)
		}
		req.RequestKey = "deleted-source"
		_, err = c.CreateVersion(ctx, req)
		expectError(t, err, 404, api.CodeResourceNotFound)
		req.RequestKey = "publish"
		req.ExpectedDraftRevision = revision(2)
		_, err = c.CreateVersion(ctx, req)
		expectError(t, err, 409, api.CodeIdempotentParameterMismatch)
		_, err = c.DeleteVersion(
			ctx,
			&api.DeleteVersionRequest{
				VersionPath: api.VersionPath{
					ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
					VersionID:    first.ID,
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		req.ExpectedDraftRevision = revision(1)
		_, err = c.CreateVersion(ctx, req)
		expectError(t, err, 410, api.CodeVersionDeleted)
	})
}

func TestDraftPublicationValidationAndRetention(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "draft-retention", "config")
		base := server.URL + "/v1/artifacts/" + a.ID + "/drafts"
		var draft struct {
			ID string `json:"id"`
		}
		data := draftPublishHTTP(t, "POST", base, `{"requestKey":"draft","payload":{"ready":true}}`, 201)
		if err := json.Unmarshal(data, &draft); err != nil {
			t.Fatal(err)
		}
		for i, extra := range []string{`"draftID":null`, `"expectedDraftRevision":1`, `"draftID":"x"`, `"draftID":"x","expectedDraftRevision":0`, `"draftID":"x","expectedDraftRevision":1,"files":[]`, `"draftID":"x","expectedDraftRevision":1,"payload":{}`, `"draftID":"x","expectedDraftRevision":1,"annotations":{}`} {
			publishRaw(
				t,
				server.URL,
				a.ID,
				"tenant",
				fmt.Sprintf(`{"requestKey":"bad-%d",%s}`, i, extra),
				400,
			)
		}
		_, err := c.UpdateRetention(ctx, policyRequest(a.ID, 0, revision(1), api.OverflowReject))
		if err != nil {
			t.Fatal(err)
		}
		first := publishVersion(t, c, a.ID, "first")
		req := &api.CreateVersionRequest{
			ArtifactPath:          api.ArtifactPath{ArtifactID: a.ID},
			RequestKey:            "draft-publish",
			DraftID:               &draft.ID,
			ExpectedDraftRevision: revision(1),
			Aliases:               []api.AliasUpdate{{Name: "stable", ExpectedRevision: revision(0)}},
		}
		_, err = c.CreateVersion(ctx, req)
		expectError(t, err, 409, api.CodeRetentionBlocked)
		_, err = c.UpdateRetention(ctx, policyRequest(a.ID, 1, revision(1), api.OverflowDeleteOldest))
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.CreateVersion(ctx, req)
		if err != nil || got.Sequence != 2 {
			t.Fatalf("retry %+v %v", got, err)
		}
		_, err = c.GetVersion(
			ctx,
			&api.GetVersionRequest{
				VersionPath: api.VersionPath{
					ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
					VersionID:    first.ID,
				},
			},
		)
		expectError(t, err, 404, api.CodeResourceNotFound)
		replay, err := c.CreateVersion(ctx, req)
		if err != nil || replay.ID != got.ID {
			t.Fatalf("alias replay %+v %v", replay, err)
		}
	})
}
