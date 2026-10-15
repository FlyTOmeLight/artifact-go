package sdk_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	serr "github.com/FlyTOmeLight/server-go/errors"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func TestDraftWireContract(t *testing.T) {
	calls := 0
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/artifacts/a/drafts" {
				t.Errorf("create %s %s", r.Method, r.URL)
			}
			fmt.Fprint(
				w,
				`{"id":"d","artifactID":"a","revision":1,"payload":{"n":9007199254740993},"annotations":{},"files":[]}`,
			)
		case 2:
			if r.Method != "GET" || r.URL.Query().Get("limit") != "2" ||
				r.URL.Query().Get("cursor") != "next" {
				t.Errorf("list %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"items":[],"nextCursor":"end"}`)
		case 3:
			if r.Method != "GET" || r.URL.Path != "/v1/artifacts/a/drafts/d" {
				t.Errorf("get %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"id":"d","revision":1}`)
		case 4:
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Method != "PUT" || string(body["files"]) != "[]" ||
				string(body["expectedRevision"]) != "1" {
				t.Errorf("update %s %s %v", r.Method, r.URL, body)
			}
			fmt.Fprint(w, `{"appliedRevision":2,"draft":{"id":"d","revision":3}}`)
		case 5:
			if r.Method != "DELETE" || r.URL.Query().Get("expectedRevision") != "1" || r.ContentLength > 0 {
				t.Errorf("delete %s %s", r.Method, r.URL)
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(410)
			fmt.Fprint(
				w,
				`{"status":410,"code":"DraftDeleted","message":"deleted","draft":{"artifactID":"a","id":"d"},"appliedRevision":2}`,
			)
		}
	}, 0)
	p := api.DraftPath{ArtifactPath: api.ArtifactPath{ArtifactID: "a"}, DraftID: "d"}
	rev := int64(1)
	created, err := c.CreateDraft(
		t.Context(),
		&api.CreateDraftRequest{
			ArtifactPath: p.ArtifactPath,
			RequestKey:   "create",
			Payload:      json.RawMessage(`{}`),
			Annotations:  json.RawMessage(`{}`),
		},
	)
	if err != nil || string(created.Payload) != `{"n":9007199254740993}` {
		t.Fatalf("create %+v %v", created, err)
	}
	if _, err = c.ListDrafts(
		t.Context(),
		&api.ListDraftsRequest{
			ArtifactPath: p.ArtifactPath,
			PageQuery:    api.PageQuery{Limit: 2, Cursor: "next"},
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetDraft(t.Context(), &api.GetDraftRequest{DraftPath: p}); err != nil {
		t.Fatal(err)
	}
	req := &api.UpdateDraftRequest{
		DraftPath:        p,
		RequestKey:       "save",
		ExpectedRevision: &rev,
		Payload:          json.RawMessage(`{}`),
		Annotations:      json.RawMessage(`{}`),
		Files:            []api.FileReference{},
	}
	saved, err := c.UpdateDraft(t.Context(), req)
	if err != nil || saved.AppliedRevision != 2 || saved.Draft.Revision != 3 {
		t.Fatalf("save %+v %v", saved, err)
	}
	if err = c.DeleteDraft(t.Context(), &api.DeleteDraftRequest{DraftPath: p, ExpectedRevision: &rev}); err != nil {
		t.Fatal(err)
	}
	_, err = c.UpdateDraft(t.Context(), req)
	var deleted *api.DraftDeletedError
	var cause *serr.Error
	if !errors.As(err, &deleted) || !errors.As(err, &cause) || deleted.Draft.ID != "d" ||
		deleted.AppliedRevision == nil ||
		*deleted.AppliedRevision != 2 ||
		cause.Code != api.CodeDraftDeleted {
		t.Fatalf("deleted %#v", err)
	}
}
