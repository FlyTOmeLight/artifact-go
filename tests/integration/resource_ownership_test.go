package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	serr "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	files "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// Controlled credentials authorize the two fixture members, never arbitrary owners.
func resourceResolver(r *http.Request) (service.RequestIdentity, error) {
	actor := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	active := true
	if actor == "historical-a" {
		actor = "user-a"
		active = false
	}
	if actor != "user-a" && actor != "user-b" {
		return service.RequestIdentity{}, serr.NewError(401, "Unauthorized", "unknown actor")
	}
	i := service.RequestIdentity{UserID: actor, TenantID: tenantA, OwnershipState: service.OwnershipActive}
	if !active {
		i.OwnershipState = service.OwnershipDeleted
	}
	if p := r.Header.Get("X-Project-ID"); p != "" {
		if p != projectA {
			return i, serr.NewError(403, "ScopeAccessDenied", "unknown project")
		}
		i.ProjectID = &p
	}
	if u := r.Header.Get("X-Resource-User-ID"); u != "" {
		if u != actor {
			return i, serr.NewError(403, "ScopeAccessDenied", "different owner")
		}
		i.ResourceUserID = &u
	}
	return i, nil
}

type selectedResource struct {
	actor         string
	project, user *string
}

func resourceContext(t *testing.T, s selectedResource) context.Context {
	t.Helper()
	return api.WithRequestIdentity(
		t.Context(),
		api.RequestIdentity{
			BearerToken:    s.actor,
			TenantID:       tenantA,
			ProjectID:      s.project,
			ResourceUserID: s.user,
		},
	)
}

func resourceRaw(
	t *testing.T,
	server *httptest.Server,
	s selectedResource,
	method, path, body string,
	want int,
) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.actor)
	req.Header.Set("Content-Type", "application/json")
	if s.project != nil {
		req.Header.Set("X-Project-ID", *s.project)
	}
	if s.user != nil {
		req.Header.Set("X-Resource-User-ID", *s.user)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("%s %s = %d want %d: %s", method, path, res.StatusCode, want, data)
	}
	return data
}

func TestOptionalResourceUserIsolation(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resourceResolver)
		client := versionClient(t, server.URL)
		userA, userB, project := "user-a", "user-b", projectA
		scopes := []selectedResource{
			{actor: userA},
			{actor: userA, project: &project},
			{actor: userA, user: &userA},
			{actor: userA, project: &project, user: &userA},
			{actor: userB, user: &userB},
			{actor: userB, project: &project, user: &userB},
		}
		rows := make([]*api.ArtifactResponse, len(scopes))
		for n, s := range scopes {
			ctx := resourceContext(t, s)
			req := &api.CreateArtifactRequest{
				Name:          "same-name",
				Type:          "memory",
				VersionScheme: api.VersionSchemeInteger,
				RequestKey:    "same-key",
			}
			row, err := client.CreateArtifact(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			rows[n] = row
			if (row.UserID == nil) != (s.user == nil) || row.CreatedBy != s.actor ||
				(row.UserID != nil && *row.UserID != *s.user) {
				t.Fatalf("ownership %+v", row)
			}
			_, err = client.CreateArtifact(
				ctx,
				&api.CreateArtifactRequest{
					Name:          "same-name",
					Type:          "memory",
					VersionScheme: api.VersionSchemeInteger,
					RequestKey:    "different-key",
				},
			)
			expectError(t, err, 409, "ArtifactNameConflict")
			list, err := client.ListArtifacts(ctx, &api.ListArtifactsRequest{})
			if err != nil || len(list.Items) != 1 || list.Items[0].ID != row.ID {
				t.Fatalf("exact list: %+v %v", list, err)
			}
		}
		for n, s := range scopes {
			for m, row := range rows {
				if n == m {
					continue
				}
				_, err := client.GetArtifact(
					resourceContext(t, s),
					&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: row.ID}},
				)
				expectError(t, err, 404, "")
			}
		}
		own := scopes[3]
		ctx := resourceContext(t, own)
		a := rows[3]
		path := api.ArtifactPath{ArtifactID: a.ID}
		versionReq := &api.CreateVersionRequest{ArtifactPath: path, RequestKey: "published"}
		v, err := client.CreateVersion(ctx, versionReq)
		if err != nil {
			t.Fatal(err)
		}
		d, err := client.CreateDraft(ctx, &api.CreateDraftRequest{ArtifactPath: path, RequestKey: "draft"})
		if err != nil {
			t.Fatal(err)
		}
		prefix := "/v1/artifacts/" + a.ID
		// Read and mutation bodies are valid, so failures must come from parent ownership.
		for _, wrong := range []selectedResource{scopes[1], scopes[2], scopes[5]} {
			for _, tc := range []struct{ method, path, body string }{
				{method: "GET", path: prefix, body: ""},
				{method: "POST", path: prefix + "/archive", body: ""},
				{method: "GET", path: prefix + "/versions", body: ""},
				{method: "GET", path: prefix + "/versions/" + v.ID, body: ""},
				{method: "POST", path: prefix + "/versions", body: `{"requestKey":"published"}`},
				{method: "GET", path: prefix + "/drafts", body: ""},
				{method: "GET", path: prefix + "/drafts/" + d.ID, body: ""},
				{method: "POST", path: prefix + "/drafts", body: `{"requestKey":"draft"}`},
				{method: "PUT", path: prefix + "/drafts/" + d.ID, body: `{"requestKey":"save","expectedRevision":1,"payload":{},"annotations":{},"files":[]}`},
				{method: "DELETE", path: prefix + "/drafts/" + d.ID + "?expectedRevision=1", body: ""},
				{method: "GET", path: prefix + "/aliases/latest", body: ""},
				{method: "PUT", path: prefix + "/aliases/stable", body: fmt.Sprintf(`{"expectedRevision":0,"versionID":%q}`, v.ID)},
				{method: "DELETE", path: prefix + "/aliases/stable?expectedRevision=1", body: ""},
				{method: "PATCH", path: prefix + "/versions/" + v.ID + "/annotations", body: `{"expectedRevision":1,"annotations":{}}`},
				{method: "PATCH", path: prefix + "/versions/" + v.ID + "/protection", body: `{"expectedRevision":1,"protected":true}`},
				{method: "DELETE", path: prefix + "/versions/" + v.ID + "?expectedRevision=1", body: ""},
				{method: "PUT", path: prefix + "/retention-policy", body: `{"expectedRevision":0,"enabled":false,"maxVersions":null,"maxAgeSeconds":null,"minVersions":1,"overflowAction":"reject","protectAliases":true}`},
				{method: "POST", path: prefix + "/retention-policy/preview", body: `{}`},
				{method: "POST", path: "/v1/resolve", body: fmt.Sprintf(`{"uri":%q}`, v.VersionURI)},
			} {
				resourceRaw(t, server, wrong, tc.method, tc.path, tc.body, 404)
			}
		}
		create(t, client, ctx, "second", "memory")
		page, err := client.ListArtifacts(ctx, &api.ListArtifactsRequest{PageQuery: api.PageQuery{Limit: 1}})
		if err != nil || page.NextCursor == "" {
			t.Fatalf("page %+v %v", page, err)
		}
		for _, wrong := range []selectedResource{scopes[1], scopes[2], scopes[5]} {
			_, err = client.ListArtifacts(
				resourceContext(t, wrong),
				&api.ListArtifactsRequest{PageQuery: api.PageQuery{Limit: 1, Cursor: page.NextCursor}},
			)
			expectError(t, err, 400, "InvalidCursor")
		}
		// Shared ownership survives changes of actor; neither member impersonates the creator.
		shared := rows[1]
		b := selectedResource{actor: userB, project: &project}
		out, err := client.CreateVersion(
			resourceContext(t, b),
			&api.CreateVersionRequest{
				ArtifactPath: api.ArtifactPath{ArtifactID: shared.ID},
				RequestKey:   "published",
			},
		)
		if err != nil || out.CreatedBy != userB {
			t.Fatalf("shared update %+v %v", out, err)
		}
		read, err := client.GetArtifact(
			resourceContext(t, b),
			&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: shared.ID}},
		)
		if err != nil || read.UserID != nil || read.CreatedBy != userA {
			t.Fatalf("shared ownership %+v %v", read, err)
		}
		// A legacy resolver cannot ignore the new selector and return shared data.
		old := fixture.HTTP(t, database, resolve)
		resourceRaw(t, old, selectedResource{actor: "tenant", user: &userA}, "GET", "/v1/artifacts", "", 403)
		empty := ""
		resourceRaw(t, server, selectedResource{actor: userA, user: &empty}, "GET", "/v1/artifacts", "", 400)
		resourceRaw(t, server, selectedResource{actor: userA, user: &userB}, "GET", "/v1/artifacts", "", 403)
	})
}

func TestCollectionCreateIdempotency(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resourceResolver)
		client := versionClient(t, server.URL)
		s := selectedResource{actor: "user-a"}
		ctx := resourceContext(t, s)
		body := `{"type":"memory","name":"Replay","versionScheme":"integer","requestKey":"operation"}`
		raw := resourceRaw(t, server, s, "POST", "/v1/artifacts", body, 201)
		var first api.ArtifactResponse
		if err := json.Unmarshal(raw, &first); err != nil {
			t.Fatal(err)
		}
		// Discarding the first response models a caller that retries after response loss.
		replay := resourceRaw(
			t,
			server,
			s,
			"POST",
			"/v1/artifacts",
			strings.Replace(body, "Replay", "replay", 1),
			200,
		)
		var second api.ArtifactResponse
		_ = json.Unmarshal(replay, &second)
		if first.ID != second.ID {
			t.Fatal("duplicate on replay")
		}
		resourceRaw(t, server, s, "POST", "/v1/artifacts", strings.Replace(body, "Replay", "changed", 1), 409)
		resourceRaw(
			t,
			server,
			s,
			"POST",
			"/v1/artifacts",
			strings.Replace(body, "operation", "another-key", 1),
			409,
		)
		req := &api.CreateArtifactRequest{
			Name:          "race",
			Type:          "memory",
			VersionScheme: api.VersionSchemeInteger,
			RequestKey:    "concurrent",
		}
		start := make(chan struct{})
		out := make(chan *api.ArtifactResponse, 8)
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; r, e := client.CreateArtifact(ctx, req); out <- r; errs <- e }()
		}
		close(start)
		wg.Wait()
		close(out)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		id := ""
		for row := range out {
			if id != "" && id != row.ID {
				t.Fatal("concurrent duplicate")
			}
			id = row.ID
		}
		_, err := client.ArchiveArtifact(
			ctx,
			&api.ArchiveArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: first.ID}},
		)
		if err != nil {
			t.Fatal(err)
		}
		resourceRaw(
			t,
			server,
			s,
			"PUT",
			"/v1/artifacts/"+first.ID+"/retention-policy",
			`{"expectedRevision":0,"enabled":false,"maxVersions":null,"maxAgeSeconds":null,"minVersions":2,"overflowAction":"reject","protectAliases":true}`,
			200,
		)
		replay = resourceRaw(
			t,
			server,
			selectedResource{actor: "historical-a"},
			"POST",
			"/v1/artifacts",
			body,
			200,
		)
		_ = json.Unmarshal(replay, &second)
		if second.State != api.ArtifactArchived || second.RetentionPolicy.Revision != 1 ||
			second.RetentionPolicy.MinVersions != 2 {
			t.Fatalf("stale replay %+v", second)
		}
		resourceRaw(
			t,
			server,
			selectedResource{actor: "historical-a"},
			"POST",
			"/v1/artifacts",
			strings.Replace(body, "operation", "new-key", 1),
			403,
		)
		// Same key is independent for another actual actor even in the same shared scope.
		resourceRaw(
			t,
			server,
			selectedResource{actor: "user-b"},
			"POST",
			"/v1/artifacts",
			strings.Replace(body, "Replay", "other-actor", 1),
			201,
		)
		for n, key := range []string{"Case", "case", "case "} {
			resourceRaw(
				t,
				server,
				s,
				"POST",
				"/v1/artifacts",
				fmt.Sprintf(
					`{"type":"memory","name":"key-%d","versionScheme":"integer","requestKey":%q}`,
					n,
					key,
				),
				201,
			)
		}
		for n, key := range []string{``, `,"requestKey":null`, `,"requestKey":""`} {
			resourceRaw(
				t,
				server,
				s,
				"POST",
				"/v1/artifacts",
				fmt.Sprintf(`{"type":"memory","name":"legacy-%d","versionScheme":"integer"%s}`, n, key),
				201,
			)
		}
	})
}

func TestCollectionCreateUnknownCommit(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resourceResolver)
		client := versionClient(t, server.URL)
		ctx := resourceContext(t, selectedResource{actor: "user-a"})
		conn, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		defer func() { database.Statement.ConnPool = original }()
		for _, keyed := range []bool{true, false} {
			for _, persist := range []bool{true, false} {
				name := fmt.Sprintf("unknown-%t-%t", keyed, persist)
				req := &api.CreateArtifactRequest{
					Name:          name,
					Type:          "memory",
					VersionScheme: api.VersionSchemeInteger,
				}
				if keyed {
					req.RequestKey = name
				}
				database.Statement.ConnPool = &uncertainCommitPool{
					ConnPool:   conn,
					connection: conn,
					persist:    persist,
				}
				got, err := client.CreateArtifact(ctx, req)
				database.Statement.ConnPool = original
				if keyed && persist {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					expectError(t, err, 503, "OperationOutcomeUnknown")
				}
				if keyed {
					replay, e := client.CreateArtifact(ctx, req)
					if e != nil {
						t.Fatal(e)
					}
					if got != nil && got.ID != replay.ID {
						t.Fatal("unknown commit duplicated")
					}
				}
				var count int64
				database.Model(&table.Artifact{}).Where("name = ?", name).Count(&count)
				want := int64(0)
				if keyed || persist {
					want = 1
				}
				if count != want {
					t.Fatalf("rows %s=%d want %d", name, count, want)
				}
			}
		}
	})
}

func TestResourceFileAndPublicationIsolation(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		driver := db.DriverPostgreSQL
		if database.Dialector.Name() == "mysql" {
			driver = db.DriverMySQL
		}
		storageDB := fixture.Open(t, driver)
		backend := httptest.NewServer(&storageFileS3{})
		defer backend.Close()
		sts := &grantSTS{}
		issuer := httptest.NewServer(sts)
		defer issuer.Close()
		svc, err := service.New(
			t.Context(),
			database,
			service.Options{
				IdentityResolver:      resourceResolver,
				AutoMigrate:           true,
				StorageDB:             storageDB,
				StorageAutoMigrate:    true,
				StorageConsumerID:     "artifact",
				TenantServiceVerifier: func(*http.Request) error { return nil },
				Storage: &service.StorageConfig{
					BackendID:       "fixture",
					Endpoint:        backend.URL,
					Region:          "us-east-1",
					AccessKeyID:     "admin",
					SecretAccessKey: "fixture",
					UsePathStyle:    true,
					GrantIssuer: &service.StorageGrantIssuerConfig{
						Endpoint:        issuer.URL,
						AccessKeyID:     "issuer",
						SecretAccessKey: "fixture",
					},
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		manager, err := conf.NewManagerFromConfig(
			&conf.Config{AppName: "scope-test", AppCode: 100, RunMode: conf.RunModeTest},
		)
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close(context.Background())
		log, err := logger.NewLogger(&logger.Config{Level: "error"})
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		h, err := host.NewHTTPHost(manager, log, nil, svc)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		server := httptest.NewServer(h.Handler())
		defer server.Close()
		fc, err := files.NewClient(files.ClientConfig{Endpoint: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		defer fc.Close()
		ac := versionClient(t, server.URL)
		user, project := "user-a", projectA
		own := selectedResource{actor: user, project: &project, user: &user}
		shared := selectedResource{actor: user, project: &project}
		fileCtx := files.WithRequestIdentity(
			t.Context(),
			files.RequestIdentity{BearerToken: user, ProjectID: project, ResourceUserID: &user},
		)
		f := storageRegister(t, fc, fileCtx, "same-key", 0)
		if f.UserID == nil || *f.UserID != user || f.CreatedBy != user {
			t.Fatalf("file owner %+v", f)
		}
		sharedCtx := files.WithRequestIdentity(
			t.Context(),
			files.RequestIdentity{BearerToken: user, ProjectID: project},
		)
		sharedFile := storageRegister(t, fc, sharedCtx, "same-key", 0)
		if sharedFile.FileID == f.FileID || sharedFile.UserID != nil {
			t.Fatal("file scope mixed")
		}
		resourceRaw(t, server, shared, "GET", "/v1/storage/files/"+f.FileID, "", 404)
		resourceRaw(t, server, shared, "DELETE", "/v1/storage/files/"+f.FileID, "", 404)
		data := resourceRaw(
			t,
			server,
			shared,
			"POST",
			"/v1/storage/files/complete",
			fmt.Sprintf(`[%q]`, f.FileID),
			200,
		)
		if !strings.Contains(string(data), "FileNotFound") {
			t.Fatalf("cross-scope complete: %s", data)
		}
		resourceRaw(
			t,
			server,
			shared,
			"POST",
			"/v1/storage/grants",
			fmt.Sprintf(`{"fileIDs":[%q],"scope":{"preset":"upload"}}`, f.FileID),
			404,
		)
		resourceRaw(
			t,
			server,
			own,
			"POST",
			"/v1/storage/grants",
			fmt.Sprintf(`{"fileIDs":[%q],"scope":{"preset":"upload"}}`, f.FileID),
			200,
		)
		done, err := fc.CompleteFiles(fileCtx, files.CompleteFilesRequest{f.FileID})
		if err != nil || done[0].Error != nil {
			t.Fatalf("complete %+v %v", done, err)
		}
		a := create(t, ac, resourceContext(t, own), "private", "memory")
		sharedA := create(t, ac, resourceContext(t, shared), "shared", "memory")
		body := fmt.Sprintf(`{"requestKey":"publish","files":[{"path":"file","fileID":%q}]}`, f.FileID)
		resourceRaw(t, server, shared, "POST", "/v1/artifacts/"+sharedA.ID+"/versions", body, 404)
		resourceRaw(t, server, own, "POST", "/v1/artifacts/"+a.ID+"/versions", body, 201)
		resourceRaw(
			t,
			server,
			own,
			"POST",
			"/v1/storage/grants",
			fmt.Sprintf(`{"fileIDs":[%q],"scope":{"preset":"download"}}`, f.FileID),
			200,
		)
		var row st.StorageFile
		if err := storageDB.First(&row, "id = ?", f.FileID).Error; err != nil {
			t.Fatal(err)
		}
		binding := storage.NewBindingService(storageDB, "artifact")
		_, err = binding.Metadata(
			t.Context(),
			storage.Scope{TenantID: tenantA, ProjectID: &project},
			[]string{f.FileID},
		)
		expectError(t, err, 404, "FileNotFound")
		_, err = binding.Metadata(
			t.Context(),
			storage.Scope{TenantID: tenantA, ProjectID: &project, UserID: &user},
			[]string{f.FileID},
		)
		if err != nil {
			t.Fatal(err)
		}
		// Persisted plans retain the resource owner for out-of-request release/recovery.
		var changes []table.ReferenceChange
		if err := database.Where("artifact_id = ?", a.ID).Find(&changes).Error; err != nil {
			t.Fatal(err)
		}
		if len(changes) == 0 {
			t.Fatal("missing durable reference plan")
		}
		for _, change := range changes {
			var plan struct {
				Scope storage.Scope `json:"scope"`
			}
			if err := json.Unmarshal(change.Plan, &plan); err != nil {
				t.Fatal(err)
			}
			if plan.Scope.UserID == nil || *plan.Scope.UserID != user {
				t.Fatal("reference plan lost personal owner")
			}
		}
	})
}

func TestResourceOwnershipMigration(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		m := database
		if database.Dialector.Name() == "mysql" {
			m = m.Set(
				"gorm:table_options",
				"ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin",
			)
		}
		if err := m.AutoMigrate(&legacyArtifactIssue2{}, &legacyFileIssue2{}); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		old := legacyArtifactIssue2{
			ID:             "old-artifact",
			TenantID:       tenantA,
			ScopeKey:       "tenant",
			Type:           "memory",
			Name:           "legacy",
			VersionScheme:  "integer",
			InitialVersion: "1",
			State:          "active",
			CreatedBy:      "user-a",
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := database.Create(&old).Error; err != nil {
			t.Fatal(err)
		}
		oldFile := legacyFileIssue2{
			ID:          "old-file",
			TenantID:    tenantA,
			ScopeKey:    "tenant",
			CreatedBy:   "user-a",
			RequestKey:  "old-file-key",
			RequestHash: "old-hash",
			BackendID:   "fixture",
			Bucket:      "bucket",
			ObjectKey:   "file",
			ContentType: "text/plain",
			State:       "ready",
			ExpiresAt:   now.Add(time.Hour),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := database.Create(&oldFile).Error; err != nil {
			t.Fatal(err)
		}
		if err := table.Migrate(t.Context(), database); err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(t.Context(), database); err != nil {
			t.Fatal(err)
		}
		var a table.Artifact
		var f st.StorageFile
		if err := database.First(&a, "id = ?", old.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.First(&f, "id = ?", oldFile.ID).Error; err != nil {
			t.Fatal(err)
		}
		if a.UserID != nil || f.UserID != nil || a.ScopeKey != "tenant" || f.ScopeKey != "tenant" ||
			a.RequestKey != nil {
			t.Fatalf("migration changed shared ownership: %+v %+v", a, f)
		}
		server := fixture.HTTP(t, database, resourceResolver)
		client := versionClient(t, server.URL)
		read, err := client.GetArtifact(
			resourceContext(t, selectedResource{actor: "user-b"}),
			&api.GetArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: a.ID}},
		)
		if err != nil || read.CreatedBy != "user-a" || read.UserID != nil {
			t.Fatalf("legacy shared access %+v %v", read, err)
		}
		user := "user-a"
		private := create(
			t,
			client,
			resourceContext(t, selectedResource{actor: user, user: &user}),
			"legacy",
			"memory",
		)
		if private.ID == a.ID {
			t.Fatal("migration mixed scopes")
		}
	})
}

type legacyArtifactIssue2 struct {
	ID              string  `gorm:"size:36;primaryKey"`
	TenantID        string  `gorm:"size:64;not null;uniqueIndex:uk_artifact_name,priority:1"`
	ProjectID       *string `gorm:"size:64;index"`
	ScopeKey        string  `gorm:"size:80;not null;uniqueIndex:uk_artifact_name,priority:2"`
	Type            string  `gorm:"size:64;not null;uniqueIndex:uk_artifact_name,priority:3"`
	Name            string  `gorm:"size:128;not null;uniqueIndex:uk_artifact_name,priority:4"`
	VersionScheme   string  `gorm:"size:16;not null"`
	InitialVersion  string  `gorm:"size:64;not null"`
	LastSequence    int64   `gorm:"not null"`
	LastMajor       int64   `gorm:"not null"`
	LastMinor       int64   `gorm:"not null"`
	LastPatch       int64   `gorm:"not null"`
	State           string  `gorm:"size:16;not null"`
	LastPublishedAt *time.Time
	NextRetentionAt *time.Time `gorm:"index"`
	CreatedBy       string     `gorm:"size:64;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (legacyArtifactIssue2) TableName() string { return "artifacts" }

type legacyFileIssue2 struct {
	ID                        string  `gorm:"size:36;primaryKey"`
	TenantID                  string  `gorm:"size:64;not null;uniqueIndex:uk_storage_request,priority:1"`
	ScopeKey                  string  `gorm:"size:80;not null;uniqueIndex:uk_storage_request,priority:2"`
	CreatedBy                 string  `gorm:"size:64;not null;uniqueIndex:uk_storage_request,priority:3"`
	RequestKey                string  `gorm:"size:64;not null;uniqueIndex:uk_storage_request,priority:4"`
	ProjectID                 *string `gorm:"size:64"`
	RequestHash               string  `gorm:"size:64;not null"`
	BackendID                 string  `gorm:"size:128;not null;uniqueIndex:uk_storage_location,priority:1"`
	Bucket                    string  `gorm:"size:63;not null;uniqueIndex:uk_storage_location,priority:2"`
	ObjectKey                 string  `gorm:"size:128;not null;uniqueIndex:uk_storage_location,priority:3"`
	ExpectedSize              int64   `gorm:"not null"`
	Size                      *int64
	ContentType               string    `gorm:"size:255;not null"`
	DeclaredChecksumAlgorithm *string   `gorm:"size:32"`
	DeclaredChecksumType      *string   `gorm:"size:16"`
	DeclaredChecksumValue     *string   `gorm:"size:256"`
	State                     string    `gorm:"size:16;not null;index:idx_storage_expiry,priority:1"`
	ExpiresAt                 time.Time `gorm:"not null;index:idx_storage_expiry,priority:2"`
	LastGrantExpiresAt        *time.Time
	DeletedAt                 *time.Time
	NextSweepAt               *time.Time `gorm:"index"`
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

func (legacyFileIssue2) TableName() string { return "storage_files" }
