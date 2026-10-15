package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestDraftConflictPersistsAbort(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "draft-abort", "model")
		d, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{ArtifactPath: api.ArtifactPath{ArtifactID: a.ID}, RequestKey: "create"},
		)
		if err != nil {
			t.Fatal(err)
		}
		path := server.URL + "/v1/artifacts/" + a.ID + "/drafts/" + d.ID
		body := `{"requestKey":"conflict","expectedRevision":2,"payload":{},"annotations":{},"files":[]}`
		draftWrite(t, "PUT", path, body, 409)
		var decision table.ReferenceChange
		if err = database.Where("artifact_id = ? AND operation_kind = ? AND request_key = ?", a.ID, "update_draft", "conflict").
			First(&decision).
			Error; err != nil {
			t.Fatal(err)
		}
		if decision.State != "aborted" || decision.AppliedRevision != nil || string(decision.Plan) != "[]" ||
			decision.AttemptID == "" {
			t.Fatalf("invalid abort decision %+v", decision)
		}
		mismatch := strings.Replace(body, `"expectedRevision":2`, `"expectedRevision":1`, 1)
		if data := draftWrite(
			t,
			"PUT",
			path,
			mismatch,
			409,
		); !strings.Contains(
			string(data),
			api.CodeIdempotentParameterMismatch,
		) {
			t.Fatalf("different parameters changed aborted intent: %s", data)
		}
		// The same aborted intent may run again once its expected revision exists.
		draftWrite(
			t,
			"PUT",
			path,
			`{"requestKey":"advance","expectedRevision":1,"payload":{},"annotations":{},"files":[]}`,
			200,
		)
		data := draftWrite(t, "PUT", path, body, 200)
		var saved api.UpdateDraftResponse
		if json.Unmarshal(data, &saved) != nil || saved.AppliedRevision != 3 {
			t.Fatalf("aborted retry %s", data)
		}
	})
}

func TestDraftAbortUnknownCommit(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "draft-abort-commit", "model")
		d, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{ArtifactPath: api.ArtifactPath{ArtifactID: a.ID}, RequestKey: "create"},
		)
		if err != nil {
			t.Fatal(err)
		}
		path := server.URL + "/v1/artifacts/" + a.ID + "/drafts/" + d.ID
		conn, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		defer func() { database.Statement.ConnPool = original }()
		for _, persist := range []bool{true, false} {
			pool := &uncertainCommitPool{ConnPool: conn, connection: conn, persist: persist}
			database.Statement.ConnPool = pool
			key := fmt.Sprintf("abort-%t", persist)
			body := fmt.Sprintf(
				`{"requestKey":%q,"expectedRevision":2,"payload":{},"annotations":{},"files":[]}`,
				key,
			)
			status := 503
			code := api.CodeOperationOutcomeUnknown
			if persist {
				status = 409
				code = api.CodeRevisionConflict
			}
			data := draftWrite(t, "PUT", path, body, status)
			database.Statement.ConnPool = original
			if !strings.Contains(string(data), code) {
				t.Fatalf("unknown abort outcome %s", data)
			}
			if pool.commits.Load() > 2 {
				t.Fatalf("unknown commit caused write retry: %d commits", pool.commits.Load())
			}
			var count int64
			if err = database.Model(&table.ReferenceChange{}).
				Where("artifact_id = ? AND operation_kind = ? AND request_key = ? AND state = ?", a.ID, "update_draft", key, "aborted").
				Count(&count).
				Error; err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if persist {
				want = 1
			}
			if count != want {
				t.Fatalf("abort persistence=%d want=%d", count, want)
			}
		}
	})
}
