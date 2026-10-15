package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

func TestStorageGrantWireContract(t *testing.T) {
	for _, scope := range []api.GrantScope{{Preset: "upload"}, {Actions: []string{"s3:GetObject"}}} {
		t.Run(scope.Preset+fmt.Sprint(scope.Actions), func(t *testing.T) {
			client := storageClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/storage/grants" {
					t.Errorf("route %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer actor" ||
					r.Header.Get("X-Tenant-ID") != "tenant" ||
					r.Header.Get("X-Project-ID") != "project" {
					t.Error("missing identity")
				}
				var body api.CreateGrantRequest
				if err := json.NewDecoder(r.Body).
					Decode(&body); err != nil || len(body.FileIDs) != 2 || body.FileIDs[0] != "f2" || body.Scope.Preset != scope.Preset || body.TTLSeconds == nil ||
					*body.TTLSeconds != 1800 {
					t.Errorf("body %+v %v", body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(
					w,
					`{"endpoint":"http://s3","region":"r","forcePathStyle":true,"expiresAt":"2026-10-15T12:00:00Z","actions":["s3:GetObject"],"credentials":{"accessKeyID":"temporary","secretAccessKey":"secret","sessionToken":"token"},"files":[{"fileID":"f2","bucket":"b","objectKey":"k2"},{"fileID":"f1","bucket":"b","objectKey":"k1"}]}`,
				)
			}, 0)
			ttl := int64(1800)
			ctx := api.WithRequestIdentity(
				t.Context(),
				api.RequestIdentity{BearerToken: "actor", TenantID: "tenant", ProjectID: "project"},
			)
			result, err := client.CreateGrant(
				ctx,
				&api.CreateGrantRequest{FileIDs: []string{"f2", "f1"}, Scope: scope, TTLSeconds: &ttl},
			)
			if err != nil || result == nil || len(result.Files) != 2 || result.Files[0].FileID != "f2" ||
				result.Credentials.SessionToken != "token" ||
				!result.ForcePathStyle ||
				result.ExpiresAt.IsZero() {
				t.Fatalf("grant %+v %v", result, err)
			}
		})
	}
}

func TestStorageGrantFailureAndCancellation(t *testing.T) {
	var calls atomic.Int32
	client := storageClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
		fmt.Fprint(w, "unavailable")
	}, 0)
	result, err := client.CreateGrant(
		t.Context(),
		&api.CreateGrantRequest{FileIDs: []string{"f"}, Scope: api.GrantScope{Preset: "upload"}},
	)
	if err == nil || result != nil || calls.Load() != 1 {
		t.Fatalf("failure %+v %v calls=%d", result, err, calls.Load())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.CreateGrant(ctx, &api.CreateGrantRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation %v", err)
	}
}
