package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func draftWrite(t *testing.T, method, url, body string, status int) []byte {
	t.Helper()
	r, e := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer tenant")
	r.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Fatalf("%s status=%d want=%d body=%s", method, resp.StatusCode, status, b)
	}
	return b
}

func TestDraftLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "draft-life", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		createReq := &api.CreateDraftRequest{
			ArtifactPath: ap,
			RequestKey:   "create",
			Payload:      json.RawMessage(`{"n":9007199254740993}`),
			Annotations:  json.RawMessage(`{}`),
		}
		d, e := c.CreateDraft(ctx, createReq)
		if e != nil {
			t.Fatal(e)
		}
		if d.Revision != 1 || string(d.Payload) != `{"n":9007199254740993}` || d.Files == nil {
			t.Fatalf("draft %+v", d)
		}
		p := api.DraftPath{ArtifactPath: ap, DraftID: d.ID}
		save := &api.UpdateDraftRequest{
			DraftPath:        p,
			RequestKey:       "save",
			ExpectedRevision: revision(1),
			Payload:          json.RawMessage(`{"v":2}`),
			Annotations:      json.RawMessage(`{"note":2}`),
			Files:            []api.FileReference{},
		}
		first, e := c.UpdateDraft(ctx, save)
		if e != nil || first.AppliedRevision != 2 || first.Draft.Revision != 2 {
			t.Fatalf("save %+v %v", first, e)
		}
		next := *save
		next.RequestKey = "next"
		next.ExpectedRevision = revision(2)
		next.Payload = json.RawMessage(`{"v":3}`)
		if _, e = c.UpdateDraft(ctx, &next); e != nil {
			t.Fatal(e)
		}
		replay, e := c.UpdateDraft(ctx, save)
		if e != nil || replay.AppliedRevision != 2 || replay.Draft.Revision != 3 ||
			string(replay.Draft.Payload) != `{"v":3}` {
			t.Fatalf("replay %+v %v", replay, e)
		}
		current, e := c.CreateDraft(ctx, createReq)
		if e != nil || current.ID != d.ID || current.Revision != 3 {
			t.Fatalf("create replay %+v %v", current, e)
		}
		changed := *save
		changed.Payload = json.RawMessage(`{"v":99}`)
		_, e = c.UpdateDraft(ctx, &changed)
		expectError(t, e, 409, api.CodeIdempotentParameterMismatch)
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := next
				r.RequestKey = fmt.Sprintf("parallel-%d", i)
				r.ExpectedRevision = revision(3)
				r.Payload = json.RawMessage(fmt.Sprintf(`{"winner":%d}`, i))
				_, e := c.UpdateDraft(ctx, &r)
				results <- e
			}()
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
			t.Fatalf("wins %d", wins)
		}
		_, e = c.GetDraft(identity(t, "project"), &api.GetDraftRequest{DraftPath: p})
		expectError(t, e, 404, "")
		e = c.DeleteDraft(ctx, &api.DeleteDraftRequest{DraftPath: p, ExpectedRevision: revision(3)})
		expectError(t, e, 409, api.CodeRevisionConflict)
		if _, e = c.ArchiveArtifact(ctx, &api.ArchiveArtifactRequest{ArtifactPath: ap}); e != nil {
			t.Fatal(e)
		}
		historical := identity(t, "historical")
		if _, e = c.UpdateDraft(historical, save); e != nil {
			t.Fatalf("historical replay %v", e)
		}
		fresh := next
		fresh.RequestKey = "archived"
		fresh.ExpectedRevision = revision(4)
		_, e = c.UpdateDraft(ctx, &fresh)
		expectError(t, e, 409, api.CodeArtifactArchived)
		if e = c.DeleteDraft(
			historical,
			&api.DeleteDraftRequest{DraftPath: p, ExpectedRevision: revision(4)},
		); e != nil {
			t.Fatal(e)
		}
		_, e = c.GetDraft(ctx, &api.GetDraftRequest{DraftPath: p})
		expectError(t, e, 404, "")
		_, e = c.CreateDraft(historical, createReq)
		expectError(t, e, 410, api.CodeDraftDeleted)
		var deleted *api.DraftDeletedError
		if !errors.As(e, &deleted) || deleted.Draft.ID != d.ID || deleted.AppliedRevision != nil {
			t.Fatalf("create deleted %v", e)
		}
		_, e = c.UpdateDraft(historical, save)
		expectError(t, e, 410, api.CodeDraftDeleted)
		if !errors.As(e, &deleted) || deleted.AppliedRevision == nil || *deleted.AppliedRevision != 2 {
			t.Fatalf("save deleted %v", e)
		}
	})
}

func TestDraftCopyListAndValidation(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "draft-validation", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		base := server.URL + "/v1/artifacts/" + a.ID + "/drafts"
		v := publishVersion(t, c, a.ID, "source")
		req := &api.CreateDraftRequest{ArtifactPath: ap, RequestKey: "copy", SourceVersionID: &v.ID}
		copied, e := c.CreateDraft(ctx, req)
		if e != nil {
			t.Fatal(e)
		}
		if string(copied.Payload) != string(v.Payload) || string(copied.Annotations) != string(v.Annotations) {
			t.Fatalf("copy %+v", copied)
		}
		// Source state does not change the original copy intent on replay.
		if _, e = c.DeleteVersion(
			ctx,
			&api.DeleteVersionRequest{VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: v.ID}},
		); e != nil {
			t.Fatal(e)
		}
		if replay, e := c.CreateDraft(ctx, req); e != nil || replay.ID != copied.ID {
			t.Fatalf("copy replay %+v %v", replay, e)
		}
		for _, body := range []string{`{"requestKey":"bad","payload":null}`, `{"requestKey":"bad","payload":[]}`, `{"requestKey":"bad","files":null}`, fmt.Sprintf(`{"requestKey":"bad","sourceVersionID":%q,"files":[]}`, v.ID)} {
			draftWrite(t, "POST", base, body, 400)
		}
		p := api.DraftPath{ArtifactPath: ap, DraftID: copied.ID}
		for _, body := range []string{`{"requestKey":"bad","expectedRevision":1,"payload":{},"annotations":{}}`, `{"requestKey":"bad","expectedRevision":1,"payload":{},"annotations":{},"files":null}`, `{"requestKey":"bad","expectedRevision":0,"payload":{},"annotations":{},"files":[]}`} {
			draftWrite(t, "PUT", base+"/"+p.DraftID, body, 400)
		}
		ids := []string{copied.ID}
		for i := range 3 {
			d, e := c.CreateDraft(
				ctx,
				&api.CreateDraftRequest{ArtifactPath: ap, RequestKey: fmt.Sprintf("list-%d", i)},
			)
			if e != nil {
				t.Fatal(e)
			}
			ids = append(ids, d.ID)
		}
		sort.Strings(ids)
		cursor := ""
		var got []string
		for {
			page, e := c.ListDrafts(
				ctx,
				&api.ListDraftsRequest{
					ArtifactPath: ap,
					PageQuery:    api.PageQuery{Limit: 1, Cursor: cursor},
				},
			)
			if e != nil {
				t.Fatal(e)
			}
			for _, item := range page.Items {
				got = append(got, item.ID)
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
			other := create(t, c, ctx, "other-"+fmt.Sprint(len(got)), "model")
			_, e = c.ListDrafts(
				ctx,
				&api.ListDraftsRequest{
					ArtifactPath: api.ArtifactPath{ArtifactID: other.ID},
					PageQuery:    api.PageQuery{Cursor: cursor},
				},
			)
			expectError(t, e, 400, api.CodeInvalidCursor)
		}
		if strings.Join(got, ",") != strings.Join(ids, ",") {
			t.Fatalf("list %v want %v", got, ids)
		}
		_, e = c.ListDrafts(ctx, &api.ListDraftsRequest{ArtifactPath: ap, PageQuery: api.PageQuery{Limit: 201}})
		expectError(t, e, 400, "")
	})
}

func TestDraftJSONLimits(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTPWithOptions(
			t,
			database,
			resolve,
			&service.VersionConfig{
				MaxFilesPerVersion:  1000,
				MaxPayloadBytes:     14,
				MaxAnnotationsBytes: 14,
				MaxRequestBodyBytes: 1024,
			},
		)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "draft-limits", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		base := server.URL + "/v1/artifacts/" + a.ID + "/drafts"
		req := &api.CreateDraftRequest{
			ArtifactPath: ap,
			RequestKey:   "boundary",
			Payload:      json.RawMessage(`{"s":"123456"}`),
			Annotations:  json.RawMessage(`{}`),
		}
		d, e := c.CreateDraft(ctx, req)
		if e != nil {
			t.Fatal(e)
		}
		tooBig := *req
		tooBig.RequestKey = "too-big"
		tooBig.Payload = json.RawMessage(`{"s":"12345678"}`)
		_, e = c.CreateDraft(ctx, &tooBig)
		expectError(t, e, 413, api.CodePayloadTooLarge)
		tooBig = *req
		tooBig.RequestKey = "too-many-notes"
		tooBig.Annotations = json.RawMessage(`{"s":"12345678"}`)
		_, e = c.CreateDraft(ctx, &tooBig)
		expectError(t, e, 413, api.CodeAnnotationsTooLarge)
		save := &api.UpdateDraftRequest{
			DraftPath:        api.DraftPath{ArtifactPath: ap, DraftID: d.ID},
			RequestKey:       "too-large-save",
			ExpectedRevision: revision(1),
			Payload:          json.RawMessage(`{"s":"12345678"}`),
			Annotations:      json.RawMessage(`{}`),
			Files:            []api.FileReference{},
		}
		_, e = c.UpdateDraft(ctx, save)
		expectError(t, e, 413, api.CodePayloadTooLarge)
		current, e := c.GetDraft(ctx, &api.GetDraftRequest{DraftPath: save.DraftPath})
		if e != nil || current.Revision != 1 {
			t.Fatalf("failed save changed revision %+v %v", current, e)
		}
		draftWrite(
			t,
			"POST",
			base,
			`{"requestKey":"body","payload":{"s":"`+strings.Repeat("x", 1024)+`"}}`,
			413,
		)
	})
}
