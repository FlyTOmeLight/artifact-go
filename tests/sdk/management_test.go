package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func TestManagementOperations(t *testing.T) {
	zero := int64(0)
	no := false
	count := 0
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Content-Type", "application/json")
		switch count {
		case 1:
			if r.Method != "PUT" || r.URL.Path != "/v1/artifacts/a/aliases/prod" {
				t.Errorf("put %s %s", r.Method, r.URL)
			}
			var b map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&b)
			if string(b["expectedRevision"]) != "0" || string(b["versionID"]) != `"v"` {
				t.Errorf("put body %v", b)
			}
		case 2:
			if r.Method != "GET" || r.URL.Query().Get("includeDeleted") != "true" {
				t.Errorf("get %s %s", r.Method, r.URL)
			}
		case 3:
			if r.Method != "DELETE" || r.URL.Query().Get("expectedRevision") != "0" || r.ContentLength > 0 {
				t.Errorf("delete %s %s len%d", r.Method, r.URL, r.ContentLength)
			}
		case 4, 5:
			name := "annotations"
			if count == 5 {
				name = "protection"
			}
			if r.Method != "PATCH" || r.URL.Path != "/v1/artifacts/a/versions/v/"+name {
				t.Errorf("patch %s %s", r.Method, r.URL)
			}
			var b map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&b)
			if string(b["expectedRevision"]) != "0" {
				t.Errorf("revision %v", b)
			}
			if count == 5 && string(b["protected"]) != "false" {
				t.Errorf("false missing %v", b)
			}
			fmt.Fprint(w, `{"id":"v","revision":2}`)
			return
		}
		fmt.Fprint(w, `{"artifactID":"a","name":"prod","revision":1,"target":null,"deletedAt":null}`)
	}, 0)
	ctx := context.Background()
	p := api.AliasPath{ArtifactPath: api.ArtifactPath{ArtifactID: "a"}, Name: "prod"}
	vp := api.VersionPath{ArtifactPath: p.ArtifactPath, VersionID: "v"}
	if _, e := c.PutAlias(
		ctx,
		&api.PutAliasRequest{AliasPath: p, VersionID: "v", ExpectedRevision: &zero},
	); e != nil {
		t.Fatal(e)
	}
	if _, e := c.GetAlias(ctx, &api.GetAliasRequest{AliasPath: p, IncludeDeleted: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.DeleteAlias(ctx, &api.DeleteAliasRequest{AliasPath: p, ExpectedRevision: &zero}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.UpdateAnnotations(
		ctx,
		&api.UpdateAnnotationsRequest{
			VersionPath:      vp,
			ExpectedRevision: &zero,
			Annotations:      json.RawMessage(`{}`),
		},
	); e != nil {
		t.Fatal(e)
	}
	if _, e := c.UpdateProtection(
		ctx,
		&api.UpdateProtectionRequest{VersionPath: vp, ExpectedRevision: &zero, Protected: &no},
	); e != nil {
		t.Fatal(e)
	}
	if count != 5 {
		t.Fatalf("calls %d", count)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := c.GetAlias(cancelCtx, &api.GetAliasRequest{AliasPath: p}); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation %v", e)
	}
}
