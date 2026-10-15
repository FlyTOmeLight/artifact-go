package sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func TestRetentionAndDeleteRequests(t *testing.T) {
	calls := 0
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			if r.Method != "PUT" || r.URL.Path != "/v1/artifacts/a/retention-policy" {
				t.Errorf("request %s %s", r.Method, r.URL)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			for k, want := range map[string]string{"maxVersions": "null", "maxAgeSeconds": "null", "enabled": "false", "protectAliases": "false", "minVersions": "0", "expectedRevision": "0"} {
				if string(body[k]) != want {
					t.Errorf("%s=%s want %s", k, body[k], want)
				}
			}
			fmt.Fprint(w, `{"artifactID":"a","revision":1}`)
		case 2:
			if r.Method != "POST" || r.URL.Path != "/v1/artifacts/a/retention-policy/preview" ||
				r.ContentLength > 0 {
				t.Errorf("preview %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"policyRevision":1,"candidates":[],"retained":[],"blockingReasons":[]}`)
		case 3:
			if r.Method != "DELETE" || r.URL.Path != "/v1/artifacts/a/versions/v" || r.ContentLength > 0 {
				t.Errorf("delete %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"id":"v","state":"deleted"}`)
		}
	}, 0)
	zero := int64(0)
	no := false
	p := api.ArtifactPath{ArtifactID: "a"}
	if _, err := c.UpdateRetention(
		context.Background(),
		&api.UpdateRetentionRequest{
			ArtifactPath:     p,
			ExpectedRevision: &zero,
			Enabled:          &no,
			MinVersions:      &zero,
			OverflowAction:   api.OverflowReject,
			ProtectAliases:   &no,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PreviewRetention(
		context.Background(),
		&api.PreviewRetentionRequest{ArtifactPath: p},
	); err != nil {
		t.Fatal(err)
	}
	deleted, err := c.DeleteVersion(
		context.Background(),
		&api.DeleteVersionRequest{VersionPath: api.VersionPath{ArtifactPath: p, VersionID: "v"}},
	)
	if err != nil || deleted.State != api.VersionDeleted {
		t.Fatalf("delete %+v %v", deleted, err)
	}
	if calls != 3 {
		t.Fatalf("calls %d", calls)
	}
}
