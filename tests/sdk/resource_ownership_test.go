package sdk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	files "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

func TestResourceUserIdentitySnapshot(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("X-Resource-User-ID")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/artifacts" {
			w.Write([]byte(`{"items":[]}`))
		} else {
			w.Write([]byte(`{"fileID":"file"}`))
		}
	}))
	defer server.Close()
	a, err := api.NewClient(api.ClientConfig{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	f, err := files.NewClient(files.ClientConfig{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	user := "original"
	ac := api.WithRequestIdentity(context.Background(), api.RequestIdentity{ResourceUserID: &user})
	fc := files.WithRequestIdentity(context.Background(), files.RequestIdentity{ResourceUserID: &user})
	user = "mutated"
	if _, err = a.ListArtifacts(ac, &api.ListArtifactsRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.GetFile(fc, "file"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ListArtifacts(context.Background(), &api.ListArtifactsRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.GetFile(context.Background(), "file"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"original", "original", "", ""} {
		if got := <-seen; got != want {
			t.Fatalf("selector %q want %q", got, want)
		}
	}
}
