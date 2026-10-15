package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	frameworkerrors "github.com/FlyTOmeLight/server-go/errors"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func TestVersionOperations(t *testing.T) {
	var calls int
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("X-Tenant-ID") != "tenant" {
			t.Error("version request lost identity")
		}
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1, 2:
			if r.Method != "POST" || r.URL.Path != "/v1/artifacts/a01/versions" {
				t.Errorf("create: %s %s", r.Method, r.URL)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if _, exists := body["artifactID"]; exists {
				t.Error("path field leaked into JSON")
			}
			if string(body["payload"]) != `{"amount":1e0}` || string(body["annotations"]) != `null` {
				t.Errorf("lost raw JSON semantics: %s", body)
			}
			if calls == 1 {
				w.WriteHeader(http.StatusCreated)
			}
		case 3:
			if r.Method != "GET" || r.URL.Path != "/v1/artifacts/a01/versions" ||
				r.URL.Query().Get("cursor") != "a+/=" ||
				r.URL.Query().Get("number") != "1.0.0" ||
				r.URL.Query().Get("limit") != "2" {
				t.Errorf("list: %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"items":[{"id":"v01"}],"nextCursor":"next"}`)
			return
		case 4:
			if r.Method != "GET" || r.URL.Path != "/v1/artifacts/a01/versions/v01" {
				t.Errorf("get: %s %s", r.Method, r.URL)
			}
		}
		fmt.Fprint(
			w,
			`{"id":"v01","artifactID":"a01","version":"1.0.0","sequence":1,"versionUri":"artifact://tenant/model/demo/versions/1.0.0","payload":{"amount":1e0},"annotations":{},"files":[],"state":"ready"}`,
		)
	}, 0)
	ctx := api.WithRequestIdentity(
		context.Background(),
		api.RequestIdentity{BearerToken: "token", TenantID: "tenant"},
	)
	req := &api.CreateVersionRequest{
		ArtifactPath: api.ArtifactPath{ArtifactID: "a01"},
		RequestKey:   "request-1",
		Payload:      json.RawMessage(`{"amount":1e0}`),
		Annotations:  json.RawMessage(`null`),
	}
	for range 2 {
		result, err := client.CreateVersion(ctx, req)
		if err != nil || result.ID != "v01" || string(result.Payload) != `{"amount":1e0}` ||
			result.Files == nil {
			t.Fatalf("create/replay: %+v %v", result, err)
		}
	}
	listed, err := client.ListVersions(
		ctx,
		&api.ListVersionsRequest{
			ArtifactPath: req.ArtifactPath,
			PageQuery:    api.PageQuery{Cursor: "a+/=", Limit: 2},
			Number:       "1.0.0",
		},
	)
	if err != nil || len(listed.Items) != 1 || listed.NextCursor != "next" {
		t.Fatalf("list: %+v %v", listed, err)
	}
	result, err := client.GetVersion(
		ctx,
		&api.GetVersionRequest{VersionPath: api.VersionPath{ArtifactPath: req.ArtifactPath, VersionID: "v01"}},
	)
	if err != nil || result.ID != "v01" {
		t.Fatalf("get: %+v %v", result, err)
	}
}

func TestVersionDeletedIdentityAndFrameworkError(t *testing.T) {
	for _, operation := range []string{"create", "get"} {
		t.Run(operation, func(t *testing.T) {
			var calls int
			client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGone)
				fmt.Fprint(
					w,
					`{"code":"VersionDeleted","message":"deleted","requestId":"req-1","statusCode":410,"version":{"id":"v01","artifactID":"a01","version":"1","sequence":1,"versionUri":"artifact://tenant/model/demo/versions/1"}}`,
				)
			}, 0)
			var err error
			if operation == "create" {
				_, err = client.CreateVersion(
					context.Background(),
					&api.CreateVersionRequest{
						ArtifactPath: api.ArtifactPath{ArtifactID: "a01"},
						RequestKey:   "key",
					},
				)
			} else {
				_, err = client.GetVersion(
					context.Background(),
					&api.GetVersionRequest{
						VersionPath: api.VersionPath{
							ArtifactPath: api.ArtifactPath{ArtifactID: "a01"},
							VersionID:    "v01",
						},
					},
				)
			}
			var deleted *api.VersionDeletedError
			var original *frameworkerrors.Error
			if !errors.As(err, &deleted) || !errors.As(err, &original) || deleted.Version.ID != "v01" ||
				original.Status != 410 ||
				original.RequestID != "req-1" ||
				err.Error() != "deleted" ||
				calls != 1 {
				t.Fatalf("error=%#v, calls=%d", err, calls)
			}
		})
	}
}
