package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serr "github.com/FlyTOmeLight/server-go/errors"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

const (
	tenantA  = "t_000000000000000000000001"
	projectA = "p_000000000000000000000001"
)

// Tokens select pre-authorized fixture identities. Request scope headers are never trusted.
func resolve(r *http.Request) (service.RequestIdentity, error) {
	identity := service.RequestIdentity{
		UserID:         "user-a",
		TenantID:       tenantA,
		OwnershipState: service.OwnershipActive,
	}
	switch r.Header.Get("Authorization") {
	case "Bearer tenant":
	case "Bearer project":
		p := projectA
		identity.ProjectID = &p
	case "Bearer other-project":
		p := "p_000000000000000000000002"
		identity.ProjectID = &p
	case "Bearer other-tenant":
		identity.TenantID = "t_000000000000000000000002"
	case "Bearer upper-tenant":
		identity.TenantID = strings.ToUpper(tenantA)
	case "Bearer upper-project":
		p := strings.ToUpper(projectA)
		identity.ProjectID = &p
	case "Bearer historical":
		identity.OwnershipState = service.OwnershipDeleted
	default:
		return service.RequestIdentity{}, serr.NewError(401, "Unauthorized", "invalid fixture token")
	}
	return identity, nil
}

func identity(t *testing.T, token string) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return api.WithRequestIdentity(ctx, api.RequestIdentity{BearerToken: token})
}

func expectError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var detail *serr.Error
	if !errors.As(err, &detail) || detail.Status != status || (code != "" && detail.Code != code) {
		t.Fatalf("error=%#v want HTTP %d %s", err, status, code)
	}
}

func create(t *testing.T, client *api.Client, ctx context.Context, name, typ string) *api.ArtifactResponse {
	t.Helper()
	out, err := client.CreateArtifact(
		ctx,
		&api.CreateArtifactRequest{Name: name, Type: typ, VersionScheme: api.VersionSchemeInteger},
	)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestArtifactHTTPAndSDK(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client, err := api.NewClient(api.ClientConfig{Endpoint: server.URL, Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		tenant := identity(t, "tenant")
		project := identity(t, "project")
		historical := identity(t, "historical")
		t.Run("create-read-archive", func(t *testing.T) {
			row := create(t, client, tenant, "Example", "model")
			if row.Name != "example" || row.ProjectID != nil || row.TenantID != tenantA ||
				row.State != api.ArtifactActive ||
				row.InitialVersion != "1" ||
				row.CreatedBy != "user-a" ||
				row.ID == "" ||
				row.ArtifactURI == "" {
				t.Fatalf("created=%+v", row)
			}
			if row.CreatedAt.Location() != time.UTC || row.UpdatedAt.Location() != time.UTC {
				t.Fatalf("non-UTC timestamps: %+v", row)
			}
			if row.RetentionPolicy.Enabled || row.RetentionPolicy.Revision != 0 ||
				row.RetentionPolicy.UpdatedAt != nil {
				t.Fatalf("default policy=%+v", row.RetentionPolicy)
			}
			read, err := client.GetArtifact(
				tenant,
				&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: row.ID}},
			)
			if err != nil || read.ID != row.ID {
				t.Fatalf("read=%+v err=%v", read, err)
			}
			for i := 0; i < 2; i++ {
				archived, err := client.ArchiveArtifact(
					tenant,
					&api.ArchiveArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: row.ID}},
				)
				if err != nil || archived.State != api.ArtifactArchived {
					t.Fatalf("archive=%+v err=%v", archived, err)
				}
			}
			_, err = client.CreateArtifact(
				tenant,
				&api.CreateArtifactRequest{
					Name:          "EXAMPLE",
					Type:          "model",
					VersionScheme: api.VersionSchemeInteger,
				},
			)
			expectError(t, err, 409, api.CodeArtifactNameConflict)
			listed, err := client.ListArtifacts(
				tenant,
				&api.ListArtifactsRequest{State: api.ArtifactArchived},
			)
			if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != row.ID {
				t.Fatalf("archive filter=%+v err=%v", listed, err)
			}
			_, err = client.GetArtifact(
				historical,
				&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: row.ID}},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateArtifact(
				historical,
				&api.CreateArtifactRequest{
					Name:          "historical-new",
					Type:          "model",
					VersionScheme: api.VersionSchemeInteger,
				},
			)
			expectError(t, err, 403, api.CodeScopeInactive)
			for _, token := range []string{"project", "other-project", "other-tenant", "upper-tenant"} {
				_, err = client.GetArtifact(
					identity(t, token),
					&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: row.ID}},
				)
				expectError(t, err, 404, api.CodeArtifactNotFound)
			}
		})
		t.Run("concurrent-archive", func(t *testing.T) {
			row := create(t, client, tenant, "concurrent-archive", "model")
			start := make(chan struct{})
			results := make(chan error, 8)
			for range 8 {
				go func() {
					<-start
					out, err := client.ArchiveArtifact(tenant, &api.ArchiveArtifactRequest{
						ArtifactPath: api.ArtifactPath{ArtifactID: row.ID},
					})
					if err == nil && out.State != api.ArtifactArchived {
						err = fmt.Errorf("unexpected archive state: %s", out.State)
					}
					results <- err
				}()
			}
			close(start)
			for range 8 {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			read, err := client.GetArtifact(tenant, &api.GetArtifactRequest{
				ArtifactPath: api.ArtifactPath{ArtifactID: row.ID},
			})
			if err != nil || read.State != api.ArtifactArchived {
				t.Fatalf("archive not persisted: %+v %v", read, err)
			}
			again, err := client.ArchiveArtifact(tenant, &api.ArchiveArtifactRequest{
				ArtifactPath: api.ArtifactPath{ArtifactID: row.ID},
			})
			if err != nil || !again.UpdatedAt.Equal(read.UpdatedAt) {
				t.Fatalf("repeated archive changed timestamp: %+v %v", again, err)
			}
		})
		t.Run("uniqueness-and-scope", func(t *testing.T) {
			create(t, client, tenant, "unique", "model")
			create(t, client, tenant, "unique", "dataset")
			p := create(t, client, project, "unique", "model")
			create(t, client, identity(t, "other-tenant"), "unique", "model")
			create(t, client, identity(t, "upper-tenant"), "unique", "model")
			create(t, client, identity(t, "upper-project"), "unique", "model")
			_, upperErr := client.GetArtifact(
				identity(t, "upper-project"),
				&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: p.ID}},
			)
			expectError(t, upperErr, 404, api.CodeArtifactNotFound)
			if p.ProjectID == nil || *p.ProjectID != projectA {
				t.Fatalf("project=%+v", p.ProjectID)
			}
			_, err := client.CreateArtifact(
				tenant,
				&api.CreateArtifactRequest{
					Name:          "unique",
					Type:          "model",
					VersionScheme: api.VersionSchemeInteger,
				},
			)
			expectError(t, err, 409, api.CodeArtifactNameConflict)
			wrong := "p_000000000000000000000002"
			_, err = client.CreateArtifact(
				project,
				&api.CreateArtifactRequest{
					Name:          "wrong-project",
					Type:          "model",
					ProjectID:     &wrong,
					VersionScheme: api.VersionSchemeInteger,
				},
			)
			expectError(t, err, 403, api.CodeScopeMismatch)
			_, err = client.GetArtifact(
				tenant,
				&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: p.ID}},
			)
			expectError(t, err, 404, api.CodeArtifactNotFound)
		})
		t.Run("pagination", func(t *testing.T) {
			for i := 0; i < 3; i++ {
				create(t, client, tenant, fmt.Sprintf("page-%d", i), "pagination")
			}
			first, err := client.ListArtifacts(
				tenant,
				&api.ListArtifactsRequest{Type: "pagination", PageQuery: api.PageQuery{Limit: 1}},
			)
			if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
				t.Fatalf("first=%+v err=%v", first, err)
			}
			seen := map[string]bool{first.Items[0].ID: true}
			cursor := first.NextCursor
			previous := first.Items[0].ID
			for cursor != "" {
				next, err := client.ListArtifacts(
					tenant,
					&api.ListArtifactsRequest{
						Type:      "pagination",
						PageQuery: api.PageQuery{Limit: 1, Cursor: cursor},
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range next.Items {
					if seen[row.ID] || row.ID <= previous {
						t.Fatalf("duplicate or unordered %s", row.ID)
					}
					seen[row.ID] = true
					previous = row.ID
				}
				cursor = next.NextCursor
			}
			if len(seen) != 3 {
				t.Fatalf("read %d rows", len(seen))
			}
			for _, tc := range []struct {
				token, typ string
				state      api.ArtifactState
			}{{token: "tenant", typ: "model", state: ""}, {token: "project", typ: "pagination", state: ""}, {token: "other-tenant", typ: "pagination", state: ""}, {token: "tenant", typ: "pagination", state: api.ArtifactActive}} {
				_, err = client.ListArtifacts(
					identity(t, tc.token),
					&api.ListArtifactsRequest{
						Type:      tc.typ,
						State:     tc.state,
						PageQuery: api.PageQuery{Limit: 1, Cursor: first.NextCursor},
					},
				)
				expectError(t, err, 400, api.CodeInvalidCursor)
			}
		})
		t.Run("concurrent-unique", func(t *testing.T) {
			const n = 8
			start := make(chan struct{})
			results := make(chan error, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := client.CreateArtifact(
						tenant,
						&api.CreateArtifactRequest{
							Name:          "racing",
							Type:          "model",
							VersionScheme: api.VersionSchemeInteger,
						},
					)
					results <- err
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			wins := 0
			for err := range results {
				if err == nil {
					wins++
				} else {
					expectError(t, err, 409, api.CodeArtifactNameConflict)
				}
			}
			if wins != 1 {
				t.Fatalf("successful creates=%d", wins)
			}
		})

		t.Run("request-body-boundary", func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				size   int
				status int
			}{
				{name: "above-framework-default", size: (1 << 20) + 1, status: 201},
				{name: "exact-limit", size: 8 << 20, status: 201},
				{name: "over-limit", size: (8 << 20) + 1, status: 413},
			} {
				body := fmt.Sprintf(`{"name":%q,"type":"model","versionScheme":"integer"}`, tc.name)
				body += strings.Repeat(" ", tc.size-len(body))
				req, err := http.NewRequestWithContext(
					t.Context(),
					http.MethodPost,
					server.URL+"/v1/artifacts",
					strings.NewReader(body),
				)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer tenant")
				req.Header.Set("Content-Type", "application/json")
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode != tc.status {
					t.Fatalf("%s status=%d: %s", tc.name, response.StatusCode, data)
				}
				if tc.status == 413 && !bytes.Contains(data, []byte(api.CodeRequestBodyTooLarge)) {
					t.Fatalf("wrong overflow error: %s", data)
				}
			}
		})
		t.Run("input-and-authentication", func(t *testing.T) {
			for _, tc := range []struct {
				name, body, token string
				status            int
			}{
				{name: "valid", body: `{"name":"raw","type":"model","versionScheme":"semver"}`, token: "tenant", status: 201},
				{name: "null", body: `null`, token: "tenant", status: 400},
				{name: "unknown", body: `{"name":"bad","type":"model","versionScheme":"integer","extra":1}`, token: "tenant", status: 400},
				{name: "multi-document", body: `{"name":"bad","type":"model","versionScheme":"integer"} {}`, token: "tenant", status: 400},
				{name: "malformed", body: `{`, token: "tenant", status: 400},
				{name: "missing-scheme", body: `{"name":"bad","type":"model"}`, token: "tenant", status: 400},
				{name: "upper-type", body: `{"name":"bad","type":"Model","versionScheme":"integer"}`, token: "tenant", status: 400},
				{name: "trailing-space", body: `{"name":"raw ","type":"model","versionScheme":"integer"}`, token: "tenant", status: 400},
				{name: "invalid-semver", body: `{"name":"bad","type":"model","versionScheme":"semver","initialVersion":"1.0.0-rc1"}`, token: "tenant", status: 400},
				{name: "untrusted-headers", body: `{"name":"bad","type":"model","versionScheme":"integer"}`, token: "", status: 401},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req, err := http.NewRequestWithContext(
						t.Context(),
						http.MethodPost,
						server.URL+"/v1/artifacts",
						strings.NewReader(tc.body),
					)
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Tenant-ID", tenantA)
					if tc.token != "" {
						req.Header.Set("Authorization", "Bearer "+tc.token)
					}
					response, err := server.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
					if response.StatusCode != tc.status {
						t.Fatalf("status %d want %d: %s", response.StatusCode, tc.status, body)
					}
					if tc.name == "valid" &&
						!bytes.Contains(body, []byte(`"initialVersion":"1.0.0"`)) {
						t.Fatalf("semver default: %s", body)
					}
				})
			}
			for _, limit := range []int{-1, 201} {
				_, err := client.ListArtifacts(
					tenant,
					&api.ListArtifactsRequest{PageQuery: api.PageQuery{Limit: limit}},
				)
				expectError(t, err, 400, "")
			}
		})
	})
}
