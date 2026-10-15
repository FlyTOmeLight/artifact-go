package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"gorm.io/gorm/clause"

	versionapp "github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

type publicationStorage struct {
	malformedBind bool
	metadataCalls atomic.Int32
	failBindAt    int32
	expiry        time.Time
	bindCalls     atomic.Int32
	releaseCalls  atomic.Int32
	mu            sync.Mutex
	refs          map[string]string
	beforeBind    func()
	deleted       atomic.Bool
}

func (p *publicationStorage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer fixture-internal" {
		w.WriteHeader(401)
		return
	}
	if r.URL.Path == "/internal/v1/storage/reference-files/query" {
		p.metadataCalls.Add(1)
		var req storagehandoff.MetadataRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		size := int64(7)
		out := []storagehandoff.FileMetadata{}
		for _, id := range req.FileIDs {
			out = append(
				out,
				storagehandoff.FileMetadata{
					FileID: id,
					State:  "ready",
					ConfirmationMetadata: storagehandoff.ConfirmationMetadata{
						Size:              &size,
						ContentType:       "text/plain",
						OriginalExpiresAt: p.expiry,
					},
				},
			)
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	var req storagehandoff.ReferencesRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if r.URL.Path == "/internal/v1/storage/references/bind" {
		if p.malformedBind {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`not a valid error response`))
			return
		}
		if p.bindCalls.Add(1) == p.failBindAt {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"code":"FileNotBindable","message":"fixture rejection"}`))
			return
		}
		p.mu.Lock()
		for _, ref := range req.References {
			p.refs[ref.ReferenceID] = "active"
		}
		p.mu.Unlock()
		if p.beforeBind != nil {
			p.beforeBind()
		}
		size := int64(7)
		out := []storagehandoff.BindResult{}
		for _, ref := range req.References {
			out = append(
				out,
				storagehandoff.BindResult{
					ReferenceID: ref.ReferenceID,
					FileID:      ref.FileID,
					State:       "active",
					File: storagehandoff.ConfirmationMetadata{
						Size:              &size,
						ContentType:       "text/plain",
						OriginalExpiresAt: p.expiry,
					},
				},
			)
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	if r.URL.Path == "/internal/v1/storage/references/release" {
		p.releaseCalls.Add(1)
		p.mu.Lock()
		for _, ref := range req.References {
			p.refs[ref.ReferenceID] = "released"
		}
		p.mu.Unlock()
	}
	out := []storagehandoff.Status{}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ref := range req.References {
		state := p.refs[ref.ReferenceID]
		if state == "" {
			state = "absent"
		}
		requires := true
		out = append(
			out,
			storagehandoff.Status{
				ReferenceID:             ref.ReferenceID,
				FileID:                  ref.FileID,
				State:                   state,
				ReleaseComplete:         p.deleted.Load(),
				ReleaseRequiresDeletion: &requires,
			},
		)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func publicationHTTP(
	t *testing.T,
	database *db.DB,
	p http.Handler,
	limits ...*service.VersionConfig,
) (*api.Client, *httptest.Server, *storagehandoff.Client) {
	t.Helper()
	remote := httptest.NewServer(p)
	t.Cleanup(remote.Close)
	hclient, err := storagehandoff.NewClient(
		storagehandoff.ClientConfig{Endpoint: remote.URL, Token: "fixture-internal"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hclient.Close)
	var versionLimits *service.VersionConfig
	if len(limits) > 0 {
		versionLimits = limits[0]
	}
	svc, err := service.New(
		t.Context(),
		database,
		service.Options{
			IdentityResolver: resolve,
			Version:          versionLimits,
			AutoMigrate:      true,
			StorageHandoff: &service.StorageHandoffConfig{
				Endpoint: remote.URL,
				Token:    "fixture-internal",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManagerFromConfig(
		&conf.Config{AppName: "reference-test", AppCode: 100, RunMode: conf.RunModeTest},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	log, err := logger.NewLogger(&logger.Config{Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	h, err := host.NewHTTPHost(manager, log, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	server := httptest.NewServer(h.Handler())
	t.Cleanup(server.Close)
	return versionClient(t, server.URL), server, hclient
}

func TestReferencePublicationLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		p := &publicationStorage{expiry: time.Now().UTC().Add(time.Hour), refs: map[string]string{}}
		client, _, handoff := publicationHTTP(t, database, p)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "file-publication", "model")
		req := &api.CreateVersionRequest{
			ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
			RequestKey:   "file-v1",
			Files:        []api.FileReference{{FileID: "file-one", Path: "model/config.json"}},
		}
		first, err := client.CreateVersion(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Files) != 1 || first.Files[0].Size != 7 || first.Files[0].Path != req.Files[0].Path {
			t.Fatalf("manifest %+v", first.Files)
		}
		again, err := client.CreateVersion(ctx, req)
		if err != nil || again.ID != first.ID || len(again.Files) != 1 || p.bindCalls.Load() != 1 {
			t.Fatalf("replay %+v %v", again, err)
		}
		got, err := client.GetVersion(
			ctx,
			&api.GetVersionRequest{
				VersionPath: api.VersionPath{ArtifactPath: req.ArtifactPath, VersionID: first.ID},
			},
		)
		if err != nil || len(got.Files) != 1 {
			t.Fatalf("get %+v %v", got, err)
		}
		list, err := client.ListVersions(ctx, &api.ListVersionsRequest{ArtifactPath: req.ArtifactPath})
		if err != nil || len(list.Items) != 1 || len(list.Items[0].Files) != 1 {
			t.Fatalf("list %+v %v", list, err)
		}
		var change table.ReferenceChange
		if err = database.First(
			&change,
			"artifact_id = ?",
			a.ID,
		).Error; err != nil ||
			change.State != "committed" {
			t.Fatalf("decision %+v %v", change, err)
		}
		deleted, err := client.DeleteVersion(
			ctx,
			&api.DeleteVersionRequest{
				VersionPath: api.VersionPath{ArtifactPath: req.ArtifactPath, VersionID: first.ID},
			},
		)
		if err != nil || deleted.State != "deleting" {
			t.Fatalf("delete %+v %v", deleted, err)
		}
		worker, err := backgroundtask.New(database, nil)
		if err != nil {
			t.Fatal(err)
		}
		worker.WithHandoff(handoff)
		task, err := worker.Claim(t.Context())
		if err != nil || task == nil {
			t.Fatalf("claim %v", err)
		}
		if err = worker.Execute(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		var row table.Version
		_ = database.First(&row, "id = ?", first.ID).Error
		if row.State != "deleting" {
			t.Fatal("did not await physical deletion")
		}
		p.deleted.Store(true)
		if err = database.Model(&table.BackgroundTask{}).
			Where("id = ?", task.ID).
			Update("next_run_at", time.Now().Add(-time.Second)).
			Error; err != nil {
			t.Fatal(err)
		}
		task, err = worker.Claim(t.Context())
		if err != nil || task == nil {
			t.Fatalf("reclaim %v", err)
		}
		if err = worker.Execute(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		_ = database.First(&row, "id = ?", first.ID).Error
		if row.State != "deleted" {
			t.Fatal("deletion did not finish")
		}
	})
}

func TestReferencePublicationAbortAndRetry(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		p := &publicationStorage{expiry: time.Now().UTC().Add(time.Hour), refs: map[string]string{}}
		client, _, _ := publicationHTTP(t, database, p)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "file-abort", "model")
		var once sync.Once
		p.beforeBind = func() {
			once.Do(func() {
				if e := database.Model(&table.Artifact{}).
					Where("id = ?", a.ID).
					Update("state", "archived").
					Error; e != nil {
					t.Error(e)
				}
			})
		}
		req := &api.CreateVersionRequest{
			ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
			RequestKey:   "same",
			Files:        []api.FileReference{{FileID: "file-one", Path: "f"}},
		}
		_, err := client.CreateVersion(ctx, req)
		expectError(t, err, 409, "ArtifactArchived")
		var old table.ReferenceChange
		_ = database.First(&old, "artifact_id = ?", a.ID).Error
		if old.State != "aborted" {
			t.Fatalf("decision %s", old.State)
		}
		var task table.BackgroundTask
		if err = database.First(
			&task,
			"kind = ? AND target_id = ?",
			"reference-release",
			old.AttemptID,
		).Error; err != nil {
			t.Fatal(err)
		}
		if task.NotBefore.Before(p.expiry.Add(-time.Millisecond)) {
			t.Fatal("compensation shortened original preparation window")
		}
		oldPayload := string(task.Payload)
		if err = database.Model(&table.Artifact{}).
			Where("id = ?", a.ID).
			Update("state", "active").
			Error; err != nil {
			t.Fatal(err)
		}
		changed := *req
		changed.Files = nil
		_, err = client.CreateVersion(ctx, &changed)
		expectError(t, err, 409, "IdempotentParameterMismatch")
		result, err := client.CreateVersion(ctx, req)
		if err != nil || result.Sequence != 1 {
			t.Fatalf("retry %+v %v", result, err)
		}
		var current table.ReferenceChange
		_ = database.First(&current, "artifact_id = ?", a.ID).Error
		if current.AttemptID == old.AttemptID || current.State != "committed" {
			t.Fatal("old attempt reused")
		}
		_ = database.First(&task, "id = ?", task.ID).Error
		if string(task.Payload) != oldPayload {
			t.Fatal("old release plan mutated")
		}
		if p.releaseCalls.Load() != 0 {
			t.Fatal("failure released early")
		}
	})
}

func TestReferencePublicationExpiredAttempt(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		p := &publicationStorage{expiry: time.Now().UTC().Add(time.Hour), refs: map[string]string{}}
		client, _, handoff := publicationHTTP(t, database, p)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "file-expire", "model")
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		p.beforeBind = func() { once.Do(func() { close(entered); <-release }) }
		req := &api.CreateVersionRequest{
			ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
			RequestKey:   "same",
			Files:        []api.FileReference{{FileID: "file-one", Path: "f"}},
		}
		done := make(chan error, 1)
		go func() { _, err := client.CreateVersion(ctx, req); done <- err }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("bind not entered")
		}
		_, err := client.CreateVersion(ctx, req)
		expectError(t, err, 409, "RequestInProgress")
		if p.metadataCalls.Load() != 1 {
			t.Fatal("in-progress replay called storage metadata")
		}
		if err = database.Model(&table.ReferenceChange{}).
			Where("artifact_id = ?", a.ID).
			Update("deadline", time.Now().Add(-time.Second)).
			Error; err != nil {
			t.Fatal(err)
		}
		worker, err := backgroundtask.New(database, nil)
		if err != nil {
			t.Fatal(err)
		}
		worker.WithHandoff(handoff)
		if err = worker.Scan(t.Context()); err != nil {
			t.Fatal(err)
		}
		close(release)
		select {
		case err = <-done:
			expectError(t, err, 409, "RequestAttemptExpired")
		case <-time.After(5 * time.Second):
			t.Fatal("publication did not stop")
		}
		var count int64
		_ = database.Model(&table.Version{}).Where("artifact_id = ?", a.ID).Count(&count).Error
		if count != 0 {
			t.Fatal("worker published or stale attempt committed")
		}
		var old table.ReferenceChange
		_ = database.First(&old, "artifact_id = ?", a.ID).Error
		published, err := client.CreateVersion(ctx, req)
		if err != nil || published.Sequence != 1 {
			t.Fatalf("new attempt %+v %v", published, err)
		}
		var current table.ReferenceChange
		_ = database.First(&current, "artifact_id = ?", a.ID).Error
		if current.AttemptID == old.AttemptID {
			t.Fatal("attempt reused")
		}
		task, err := worker.Claim(t.Context())
		if err != nil || task != nil {
			t.Fatal("compensation ran before original expiry")
		}
	})
}

func TestReferencePublicationUnknownCommit(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		for _, persist := range []bool{true, false} {
			p := &publicationStorage{expiry: time.Now().UTC().Add(time.Hour), refs: map[string]string{}}
			client, _, _ := publicationHTTP(t, database, p)
			ctx := identity(t, "tenant")
			name := "file-unknown-rollback"
			if persist {
				name = "file-unknown-commit"
			}
			a := create(t, client, ctx, name, "model")
			conn, err := database.DB()
			if err != nil {
				t.Fatal(err)
			}
			original := database.Statement.ConnPool
			p.beforeBind = func() {
				database.Statement.ConnPool = &uncertainCommitPool{
					ConnPool:   conn,
					connection: conn,
					persist:    persist,
				}
			}
			req := &api.CreateVersionRequest{
				ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
				RequestKey:   "same",
				Files:        []api.FileReference{{FileID: "file-one", Path: "f"}},
			}
			result, err := client.CreateVersion(ctx, req)
			database.Statement.ConnPool = original
			if persist {
				if err != nil || len(result.Files) != 1 {
					t.Fatalf("committed result %+v %v", result, err)
				}
			} else {
				expectError(t, err, 503, "OperationOutcomeUnknown")
			}
			var decision table.ReferenceChange
			_ = database.First(&decision, "artifact_id = ?", a.ID).Error
			expected := "preparing"
			if persist {
				expected = "committed"
			}
			if decision.State != expected {
				t.Fatalf("decision %s want %s", decision.State, expected)
			}
			var releases int64
			_ = database.Model(&table.BackgroundTask{}).
				Where("kind = ? AND target_id = ?", "reference-release", decision.AttemptID).
				Count(&releases).
				Error
			if releases != 0 {
				t.Fatal("unknown commit incorrectly compensated")
			}
		}
	})
}

func TestReferencePlanPartialBatchCompensation(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		p := &publicationStorage{expiry: time.Now().Add(-time.Hour), refs: map[string]string{}, failBindAt: 2}
		p.deleted.Store(true)
		client, _, handoff := publicationHTTP(t, database, p)
		a := create(t, client, identity(t, "tenant"), "partial-bind", "model")
		plan := versionapp.ReferencePlan{Scope: storagehandoff.Scope{TenantID: tenantA}}
		for i := 0; i < 1001; i++ {
			plan.References = append(
				plan.References,
				versionapp.PlannedReference{
					Reference: storagehandoff.Reference{
						ReferenceID: fmt.Sprintf("ref-%d", i),
						FileID:      "file-one",
					},
					OriginalExpiresAt: p.expiry,
				},
			)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		decision := table.ReferenceChange{
			ArtifactID:    a.ID,
			ActorID:       "user-a",
			OperationKind: "create-version",
			RequestKey:    "partial",
			RequestHash:   "fixed",
			TenantID:      tenantA,
			ScopeKey:      "tenant",
			AttemptID:     "partial-attempt",
			State:         "preparing",
			Deadline:      time.Now().Add(time.Minute),
			Plan:          raw,
		}
		if err = database.Create(&decision).Error; err != nil {
			t.Fatal(err)
		}
		if _, err = plan.Bind(t.Context(), handoff); err == nil {
			t.Fatal("second batch should fail")
		}
		if p.bindCalls.Load() != 2 {
			t.Fatal("bind did not batch")
		}
		if err = db.Transaction(
			t.Context(),
			database,
			db.TransactionOptions{Isolation: sql.LevelReadCommitted},
			func(tx *db.DB) error {
				var locked table.Artifact
				if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					First(&locked, "id = ?", a.ID).
					Error; e != nil {
					return e
				}
				if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					First(&decision, "artifact_id = ?", a.ID).
					Error; e != nil {
					return e
				}
				now, e := table.DatabaseNow(tx)
				if e != nil {
					return e
				}
				return versionapp.AbortReferenceChange(tx, &decision, now)
			},
		); err != nil {
			t.Fatal(err)
		}
		worker, err := backgroundtask.New(database, nil)
		if err != nil {
			t.Fatal(err)
		}
		worker.WithHandoff(handoff)
		task, err := worker.Claim(t.Context())
		if err != nil || task == nil {
			t.Fatalf("claim %v", err)
		}
		if err = worker.Execute(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		if p.releaseCalls.Load() != 2 {
			t.Fatal("release did not include failed batch")
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.refs) != 1001 {
			t.Fatal("compensation omitted uncertain/unbound references")
		}
		for _, state := range p.refs {
			if state != "released" {
				t.Fatal("reference not released")
			}
		}
	})
}

func TestReferencePublicationMalformedRejection(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		p := &publicationStorage{
			expiry:        time.Now().UTC().Add(time.Hour),
			refs:          map[string]string{},
			malformedBind: true,
		}
		client, _, _ := publicationHTTP(t, database, p)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "malformed-bind", "model")
		req := &api.CreateVersionRequest{
			ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
			RequestKey:   "malformed",
			Files:        []api.FileReference{{FileID: "file-one", Path: "f"}},
		}
		_, err := client.CreateVersion(ctx, req)
		expectError(t, err, 503, "StorageUnavailable")
		var decision table.ReferenceChange
		if err = database.First(&decision, "artifact_id = ?", a.ID).Error; err != nil {
			t.Fatal(err)
		}
		if decision.State != "aborted" {
			t.Fatalf("expected durable abort, got %s", decision.State)
		}
		var task table.BackgroundTask
		if err = database.First(
			&task,
			"kind = ? AND target_id = ?",
			"reference-release",
			decision.AttemptID,
		).Error; err != nil {
			t.Fatal(err)
		}
		if task.NotBefore.Before(p.expiry.Add(-time.Millisecond)) {
			t.Fatal("unknown Bind outcome shortened compensation window")
		}
		var plan versionapp.ReferencePlan
		if err = json.Unmarshal(
			task.Payload,
			&plan,
		); err != nil || len(plan.References) != 1 ||
			plan.References[0].FileID != "file-one" {
			t.Fatalf("missing immutable compensation plan: %+v %v", plan, err)
		}
	})
}
