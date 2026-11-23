package integration_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestMemoryArchiveRequiresTrustedService(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, func(req *http.Request) (service.RequestIdentity, error) {
			name := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
			switch name {
			case "agentic-kit-api", "other-service":
				req = req.Clone(req.Context())
				req.Header.Set("Authorization", "Bearer user-a")
			default:
				name = ""
			}
			identity, err := resourceResolver(req)
			identity.PropagatingService = name
			return identity, err
		})
		client := versionClient(t, server.URL)
		owner, project := "user-a", projectA
		user := selectedResource{actor: owner, project: &project, user: &owner}
		trusted := selectedResource{actor: "agentic-kit-api", project: &project, user: &owner}
		other := selectedResource{actor: "other-service", project: &project, user: &owner}
		userContext := resourceContext(t, user)
		trustedContext := resourceContext(t, trusted)
		agent := create(t, client, userContext, "agent", "agent")
		memory := create(t, client, trustedContext, "memory", "agent-memory")
		path := api.ArtifactPath{ArtifactID: memory.ID}
		version, err := client.CreateVersion(trustedContext, &api.CreateVersionRequest{
			ArtifactPath: path, RequestKey: "archive-version", Payload: []byte(`{"memory":"body"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := client.Resolve(trustedContext, &api.ResolveRequest{URI: version.VersionURI})
		if err != nil || resolved.Version == nil || resolved.Version.ID != version.ID {
			t.Fatalf("trusted resolve = %+v, %v", resolved, err)
		}
		for _, actor := range []selectedResource{user, other} {
			t.Run(actor.actor, func(t *testing.T) {
				ctx := resourceContext(t, actor)
				for _, artifactType := range []string{"", "agent-memory"} {
					list, err := client.ListArtifacts(
						ctx,
						&api.ListArtifactsRequest{Type: artifactType},
					)
					if err != nil {
						t.Fatal(err)
					}
					if artifactType == "" &&
						(len(list.Items) != 1 || list.Items[0].ID != agent.ID) {
						t.Fatalf("unfiltered list = %+v", list)
					}
					if artifactType != "" && len(list.Items) != 0 {
						t.Fatalf("filtered list = %+v", list)
					}
				}
				resourceRaw(
					t,
					server,
					actor,
					"POST",
					"/v1/artifacts",
					`{"name":"another-memory","type":"agent-memory","versionScheme":"integer"}`,
					403,
				)
				prefix := "/v1/artifacts/" + memory.ID
				for _, request := range []struct{ method, path, body string }{
					{method: "GET", path: prefix},
					{method: "POST", path: prefix + "/archive"},
					{method: "GET", path: prefix + "/versions"},
					{method: "GET", path: prefix + "/versions/" + version.ID},
					{method: "POST", path: prefix + "/versions", body: `{"requestKey":"archive-version","payload":{"memory":"body"}}`},
					{method: "GET", path: prefix + "/drafts"},
					{method: "GET", path: prefix + "/aliases/latest"},
					{method: "POST", path: prefix + "/retention-policy/preview", body: `{}`},
					{method: "POST", path: "/v1/resolve", body: fmt.Sprintf(`{"uri":%q}`, version.VersionURI)},
				} {
					resourceRaw(t, server, actor, request.method, request.path, request.body, 404)
				}
			})
		}
		resourceRaw(t, server, trusted, "GET", "/v1/artifacts/"+memory.ID, "", 200)
		resourceRaw(t, server, trusted, "GET", "/v1/artifacts/"+memory.ID+"/versions/"+version.ID, "", 200)
		resourceRaw(t, server, user, "GET", "/v1/artifacts/"+agent.ID, "", 200)
	})
}
