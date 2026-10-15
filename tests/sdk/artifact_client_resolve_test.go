package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	frameworkerrors "github.com/FlyTOmeLight/server-go/errors"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func TestResolveWireContract(t *testing.T) {
	uri := "artifact://tenant/artifacts/model/a01/aliases/latest/files/dir/a%20b.json"
	var calls atomic.Int32
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/resolve" || r.URL.RawQuery != "" ||
			r.Header.Get("Authorization") != "Bearer selected" {
			t.Errorf("request %s %s %q", r.Method, r.URL, r.Header.Get("Authorization"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["uri"] != uri {
			t.Errorf("body %+v %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(
			w,
			`{"artifact":{"id":"a01"},"version":{"id":"v01","version":"3","versionUri":"artifact://tenant/artifacts/model/a01/versions/v01","payload":{"n":9007199254740993},"files":[]},"file":{"fileID":"f01","path":"dir/a b.json","size":7,"fileUri":"artifact://tenant/artifacts/model/a01/versions/v01/files/dir/a%20b.json"}}`,
		)
	}, 0)
	ctx := api.WithRequestIdentity(t.Context(), api.RequestIdentity{BearerToken: "selected"})
	got, err := client.Resolve(ctx, &api.ResolveRequest{URI: uri})
	if err != nil || got.Artifact.ID != "a01" || got.Version == nil || got.Version.ID != "v01" || got.File == nil ||
		got.File.FileID != "f01" ||
		string(got.Version.Payload) != `{"n":9007199254740993}` {
		t.Fatalf("resolve %+v %v", got, err)
	}
	for _, req := range []*api.ResolveRequest{nil, {}} {
		if _, err := client.Resolve(ctx, req); err == nil {
			t.Fatal("accepted missing URI")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid requests reached transport: %d", calls.Load())
	}
}

func TestResolveErrorsAndNoImplicitRetry(t *testing.T) {
	var calls atomic.Int32
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		fmt.Fprint(w, `{"status":503,"code":"Unavailable","message":"unavailable"}`)
	}, 0)
	_, err := client.Resolve(t.Context(), &api.ResolveRequest{URI: "artifact://tenant/artifacts/model/a"})
	var remote *frameworkerrors.Error
	if !errors.As(err, &remote) || remote.Code != "Unavailable" || calls.Load() != 1 {
		t.Fatalf("error %v calls %d", err, calls.Load())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Resolve(ctx, &api.ResolveRequest{URI: "artifact://tenant/artifacts/model/a"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
