package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	"github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	storageapi "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// The databases are deliberately separate. Only HTTP carries reference handoffs.
func fileDraftHTTP(
	t *testing.T,
	database *db.DB,
	afterBind func(),
	limits ...*service.VersionConfig,
) (*api.Client, *db.DB, *storagehandoff.Client) {
	t.Helper()
	driver := db.DriverPostgreSQL
	if database.Dialector.Name() == "mysql" {
		driver = db.DriverMySQL
	}
	storageDB := fixture.Open(t, driver)
	if err := st.Migrate(t.Context(), storageDB); err != nil {
		t.Fatal(err)
	}
	handler, err := storagehandoff.NewHandler(
		storage.NewBindingService(storageDB),
		[]storagehandoff.Consumer{
			{
				ID:     "artifact",
				Tokens: []string{"fixture-internal"},
				Scopes: []storagehandoff.Scope{{TenantID: tenantA}},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if afterBind != nil && r.URL.Path == "/internal/v1/storage/references/bind" {
			afterBind()
		}
	})
	client, _, handoff := publicationHTTP(t, database, wrapped, limits...)
	return client, storageDB, handoff
}

func draftReadyFile(t *testing.T, database *db.DB) string {
	t.Helper()
	id := bindingSeed(t, database, "ready")
	if err := database.Model(&st.StorageFile{}).Where("id = ?", id).Update("tenant_id", tenantA).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func draftRows(t *testing.T, database *db.DB, id string) []table.DraftFile {
	t.Helper()
	var rows []table.DraftFile
	if err := database.Where("draft_id = ?", id).Order("logical_path").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func runReferenceTasks(t *testing.T, database *db.DB, handoff *storagehandoff.Client) {
	t.Helper()
	worker, err := backgroundtask.New(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker.WithHandoff(handoff)
	for range 20 {
		claim, err := worker.Claim(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if claim == nil {
			return
		}
		if err = worker.Execute(t.Context(), claim); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("reference tasks did not settle")
}

func TestFileDraftFourPublicationFlows(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		client, storageDB, _ := fileDraftHTTP(t, database, nil)
		ctx := identity(t, "tenant")
		for _, scenario := range []struct {
			name  string
			paths []string
			draft bool
		}{
			{name: "single", paths: []string{"model.json"}, draft: false},
			{name: "draft", paths: []string{"config.json"}, draft: true},
			{name: "multiple", paths: []string{"model.bin", "vocab.json"}, draft: false},
			{name: "folder", paths: []string{"dataset/train/a.json", "dataset/test/a.json"}, draft: false},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				a := create(t, client, ctx, scenario.name, "model")
				ap := api.ArtifactPath{ArtifactID: a.ID}
				refs := []api.FileReference{}
				for _, path := range scenario.paths {
					refs = append(
						refs,
						api.FileReference{FileID: draftReadyFile(t, storageDB), Path: path},
					)
				}
				req := &api.CreateVersionRequest{
					ArtifactPath: ap,
					RequestKey:   "publish",
					Payload:      json.RawMessage(`{"format":"file"}`),
					Files:        refs,
				}
				var draft *api.DraftResponse
				if scenario.draft {
					var err error
					draft, err = client.CreateDraft(
						ctx,
						&api.CreateDraftRequest{
							ArtifactPath: ap,
							RequestKey:   "draft",
							Payload:      req.Payload,
							Files:        refs,
						},
					)
					if err != nil {
						t.Fatal(err)
					}
					req.Payload = nil
					req.Files = nil
					req.DraftID = &draft.ID
					req.ExpectedDraftRevision = &draft.Revision
				}
				v, err := client.CreateVersion(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				if len(v.Files) != len(refs) || string(v.Payload) != `{"format":"file"}` {
					t.Fatalf("version %+v", v)
				}
				for _, f := range v.Files {
					if f.Size != 7 {
						t.Fatalf("file metadata %+v", f)
					}
				}
				replay, err := client.CreateVersion(ctx, req)
				if err != nil || replay.ID != v.ID {
					t.Fatalf("replay %+v %v", replay, err)
				}
				if draft != nil {
					current, err := client.GetDraft(
						ctx,
						&api.GetDraftRequest{
							DraftPath: api.DraftPath{ArtifactPath: ap, DraftID: draft.ID},
						},
					)
					if err != nil || current.Revision != draft.Revision ||
						len(current.Files) != len(refs) {
						t.Fatalf("publish changed draft %+v %v", current, err)
					}
					var versionFiles []table.VersionFile
					if err := database.Where("version_id = ?", v.ID).
						Find(&versionFiles).
						Error; err != nil {
						t.Fatal(err)
					}
					if versionFiles[0].ReferenceID == draftRows(t, database, draft.ID)[0].ReferenceID {
						t.Fatal("version stole draft reference")
					}
				}
			})
		}
	})
}

func TestFileDraftReuseRenameRemoveAndLastRelease(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		client, storageDB, handoff := fileDraftHTTP(t, database, nil)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "reuse", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		file := draftReadyFile(t, storageDB)
		d, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: ap,
				RequestKey:   "draft",
				Files:        []api.FileReference{{FileID: file, Path: "old.json"}},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		dp := api.DraftPath{ArtifactPath: ap, DraftID: d.ID}
		original := draftRows(t, database, d.ID)[0].ReferenceID
		save := &api.UpdateDraftRequest{
			DraftPath:        dp,
			RequestKey:       "rename",
			ExpectedRevision: &d.Revision,
			Payload:          json.RawMessage(`{}`),
			Annotations:      json.RawMessage(`{}`),
			Files:            []api.FileReference{{FileID: file, Path: "renamed.json"}},
		}
		renamed, err := client.UpdateDraft(ctx, save)
		if err != nil {
			t.Fatal(err)
		}
		if draftRows(t, database, d.ID)[0].ReferenceID != original {
			t.Fatal("rename allocated a new holding")
		}
		v, err := client.CreateVersion(
			ctx,
			&api.CreateVersionRequest{
				ArtifactPath:          ap,
				RequestKey:            "publish",
				DraftID:               &d.ID,
				ExpectedDraftRevision: &renamed.Draft.Revision,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		copied, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{ArtifactPath: ap, RequestKey: "copy", SourceVersionID: &v.ID},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(copied.Files) != 1 || draftRows(t, database, copied.ID)[0].ReferenceID == original {
			t.Fatalf("copy %+v", copied)
		}
		b := create(t, client, ctx, "reuse-other", "model")
		other, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: api.ArtifactPath{ArtifactID: b.ID},
				RequestKey:   "reuse",
				Files:        []api.FileReference{{FileID: file, Path: "shared.json"}},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		// Removing from one draft must not reclaim bytes held by versions/other drafts.
		save.RequestKey = "remove"
		save.ExpectedRevision = &renamed.Draft.Revision
		save.Files = []api.FileReference{}
		removed, err := client.UpdateDraft(ctx, save)
		if err != nil {
			t.Fatal(err)
		}
		runReferenceTasks(t, database, handoff)
		var held st.StorageFile
		if err := storageDB.First(&held, "id = ?", file).Error; err != nil || held.State != "bound" {
			t.Fatalf("shared file %+v %v", held, err)
		}
		save.RequestKey = "readd"
		save.ExpectedRevision = &removed.Draft.Revision
		save.Files = []api.FileReference{{FileID: file, Path: "again.json"}}
		readded, err := client.UpdateDraft(ctx, save)
		if err != nil {
			t.Fatal(err)
		}
		if draftRows(t, database, d.ID)[0].ReferenceID == original {
			t.Fatal("released reference resurrected")
		}
		// Original preparation expiry no longer restricts live bound references.
		if err := storageDB.Model(&st.StorageFile{}).
			Where("id = ?", file).
			Update("expires_at", time.Now().Add(-time.Hour)).
			Error; err != nil {
			t.Fatal(err)
		}
		newer, err := client.CreateVersion(
			ctx,
			&api.CreateVersionRequest{ArtifactPath: ap, RequestKey: "bound-expired", Files: save.Files},
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, draft := range []*api.DraftResponse{&readded.Draft, copied, other} {
			if err := client.DeleteDraft(
				ctx,
				&api.DeleteDraftRequest{
					DraftPath: api.DraftPath{
						ArtifactPath: api.ArtifactPath{ArtifactID: draft.ArtifactID},
						DraftID:      draft.ID,
					},
					ExpectedRevision: &draft.Revision,
				},
			); err != nil {
				t.Fatal(err)
			}
		}
		for _, version := range []*api.VersionResponse{v, newer} {
			if _, err := client.DeleteVersion(
				ctx,
				&api.DeleteVersionRequest{
					VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: version.ID},
				},
			); err != nil {
				t.Fatal(err)
			}
		}
		// Logical deletion does not falsely claim storage has already received Release.
		grants, _, _ := grantHTTP(t, storageDB)
		read := &storageapi.CreateGrantRequest{
			FileIDs: []string{file},
			Scope:   storageapi.GrantScope{Preset: "download"},
		}
		if _, err := grants.CreateGrant(storageIdentity(t, "tenant"), read); err != nil {
			t.Fatalf("bound read before delivery: %v", err)
		}
		if err := storageDB.First(&held, "id = ?", file).Error; err != nil || held.State != "bound" {
			t.Fatalf("release before delivery %+v %v", held, err)
		}
		runReferenceTasks(t, database, handoff)
		if err := storageDB.First(&held, "id = ?", file).Error; err != nil || held.State != "deleting" {
			t.Fatalf("last release %+v %v", held, err)
		}
		if _, err := grants.CreateGrant(storageIdentity(t, "tenant"), read); err == nil {
			t.Fatal("last release still allowed a new read grant")
		}
		var active int64
		if err := storageDB.Model(&st.StorageReference{}).
			Where("file_id = ? AND state = ?", file, "active").
			Count(&active).
			Error; err != nil ||
			active != 0 {
			t.Fatalf("active refs %d %v", active, err)
		}
		if err := storageDB.Model(&st.StorageFile{}).
			Where("id = ?", file).
			Update("backend_id", "local").
			Error; err != nil {
			t.Fatal(err)
		}
		gc := gcWorker(t, storageDB, &gcObjects{exists: true})
		claim, err := gc.Claim(t.Context())
		if err != nil || claim == nil {
			t.Fatalf("gc claim %+v %v", claim, err)
		}
		if err := gc.Execute(t.Context(), claim); err != nil {
			t.Fatal(err)
		}
		if err := database.Model(&table.BackgroundTask{}).
			Where("state = ?", "pending").
			Update("next_run_at", time.Now().Add(-time.Second)).
			Error; err != nil {
			t.Fatal(err)
		}
		runReferenceTasks(t, database, handoff)
		var remaining int64
		if err := database.Model(&table.Version{}).
			Where("artifact_id = ? AND state = ?", a.ID, "deleting").
			Count(&remaining).
			Error; err != nil ||
			remaining != 0 {
			t.Fatalf("unfinished versions %d %v", remaining, err)
		}
	})
}

func TestFileDraftConcurrentEditorsCanSaveLosingAttachmentsAsNewDraft(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		entered, release := make(chan struct{}, 2), make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		var barrier atomic.Bool
		client, storageDB, handoff := fileDraftHTTP(t, database, func() {
			if barrier.Load() {
				entered <- struct{}{}
				<-release
			}
		})
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "editors", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		d, err := client.CreateDraft(ctx, &api.CreateDraftRequest{ArtifactPath: ap, RequestKey: "draft"})
		if err != nil {
			t.Fatal(err)
		}
		files := []string{draftReadyFile(t, storageDB), draftReadyFile(t, storageDB)}
		type result struct {
			index int
			err   error
		}
		barrier.Store(true)
		results := make(chan result, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := client.UpdateDraft(
					ctx,
					&api.UpdateDraftRequest{
						DraftPath:        api.DraftPath{ArtifactPath: ap, DraftID: d.ID},
						RequestKey:       fmt.Sprintf("page-%d", i),
						ExpectedRevision: &d.Revision,
						Payload:          json.RawMessage(fmt.Sprintf(`{"page":%d}`, i)),
						Annotations:      json.RawMessage(`{}`),
						Files: []api.FileReference{
							{FileID: files[i], Path: "config.json"},
						},
					},
				)
				results <- result{index: i, err: err}
			}()
		}
		for range 2 {
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				unblock()
				t.Fatal("both editor attempts did not reach independent Bind")
			}
		}
		barrier.Store(false)
		unblock()
		wg.Wait()
		close(results)
		winner, loser := -1, -1
		for result := range results {
			if result.err == nil {
				winner = result.index
			} else {
				expectError(t, result.err, 409, api.CodeRevisionConflict)
				loser = result.index
			}
		}
		if winner < 0 || loser < 0 {
			t.Fatalf("winner %d loser %d", winner, loser)
		}
		var losingChange table.ReferenceChange
		if err := database.Where("artifact_id = ? AND request_key = ?", a.ID, fmt.Sprintf("page-%d", loser)).
			First(&losingChange).
			Error; err != nil ||
			losingChange.State != "aborted" {
			t.Fatalf("losing edit decision %+v %v", losingChange, err)
		}
		var compensation table.BackgroundTask
		if err := database.Where("kind = ? AND target_id = ?", "reference-release", losingChange.AttemptID).
			First(&compensation).
			Error; err != nil {
			t.Fatal(err)
		}
		var originalWindow st.StorageFile
		if err := storageDB.First(&originalWindow, "id = ?", files[loser]).Error; err != nil {
			t.Fatal(err)
		}
		if !compensation.NotBefore.Equal(originalWindow.ExpiresAt) {
			t.Fatalf(
				"failure changed attachment window: task=%s original=%s",
				compensation.NotBefore,
				originalWindow.ExpiresAt,
			)
		}
		current, err := client.GetDraft(
			ctx,
			&api.GetDraftRequest{DraftPath: api.DraftPath{ArtifactPath: ap, DraftID: d.ID}},
		)
		if err != nil || len(current.Files) != 1 || current.Files[0].FileID != files[winner] ||
			string(current.Payload) != fmt.Sprintf(`{"page":%d}`, winner) {
			t.Fatalf("mixed snapshot %+v %v", current, err)
		}
		saved, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: ap,
				RequestKey:   "save-as",
				Payload:      json.RawMessage(fmt.Sprintf(`{"page":%d}`, loser)),
				Files:        []api.FileReference{{FileID: files[loser], Path: "config.json"}},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(saved.Files) != 1 || saved.Files[0].FileID != files[loser] {
			t.Fatalf("save-as %+v", saved)
		}
		runReferenceTasks(t, database, handoff)
		for _, file := range files {
			var row st.StorageFile
			if err := storageDB.First(&row, "id = ?", file).Error; err != nil || row.State != "bound" {
				t.Fatalf("compensation removed winner/saved attachment %+v %v", row, err)
			}
		}
	})
}

func TestFileDraftPublicationRechecksSourceAfterBind(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		for _, action := range []string{"edit", "delete"} {
			t.Run(action, func(t *testing.T) {
				var trigger atomic.Bool
				var mutate func()
				client, storageDB, _ := fileDraftHTTP(t, database, func() {
					if trigger.Swap(false) {
						mutate()
					}
				})
				ctx := identity(t, "tenant")
				a := create(t, client, ctx, "source-"+action, "model")
				ap := api.ArtifactPath{ArtifactID: a.ID}
				file := draftReadyFile(t, storageDB)
				refs := []api.FileReference{{FileID: file, Path: "config.json"}}
				d, err := client.CreateDraft(
					ctx,
					&api.CreateDraftRequest{
						ArtifactPath: ap,
						RequestKey:   "draft",
						Payload:      json.RawMessage(`{"v":1}`),
						Files:        refs,
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				dp := api.DraftPath{ArtifactPath: ap, DraftID: d.ID}
				mutationError := make(chan error, 1)
				mutate = func() {
					if action == "edit" {
						_, err := client.UpdateDraft(
							ctx,
							&api.UpdateDraftRequest{
								DraftPath:        dp,
								RequestKey:       "during-bind",
								ExpectedRevision: &d.Revision,
								Payload:          json.RawMessage(`{"v":2}`),
								Annotations:      json.RawMessage(`{}`),
								Files:            refs,
							},
						)
						mutationError <- err
					} else {
						mutationError <- client.DeleteDraft(ctx, &api.DeleteDraftRequest{DraftPath: dp, ExpectedRevision: &d.Revision})
					}
				}
				trigger.Store(true)
				_, err = client.CreateVersion(
					ctx,
					&api.CreateVersionRequest{
						ArtifactPath:          ap,
						RequestKey:            "publish",
						DraftID:               &d.ID,
						ExpectedDraftRevision: &d.Revision,
					},
				)
				if mutationErr := <-mutationError; mutationErr != nil {
					t.Fatal(mutationErr)
				}
				if action == "edit" {
					expectError(t, err, 409, api.CodeRevisionConflict)
				} else {
					expectError(t, err, 404, "")
				}
				versions, err := client.ListVersions(ctx, &api.ListVersionsRequest{ArtifactPath: ap})
				if err != nil || len(versions.Items) != 0 {
					t.Fatalf("published stale source %+v %v", versions, err)
				}
				var change table.ReferenceChange
				if err := database.Where("artifact_id = ? AND operation_kind = ?", a.ID, "create-version").
					First(&change).
					Error; err != nil ||
					change.State != "aborted" {
					t.Fatalf("decision %+v %v", change, err)
				}
			})
		}
	})
}

func TestFileDraftLimitsAndExplicitJSONFile(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		limits := &service.VersionConfig{
			MaxFilesPerVersion:  2,
			MaxPayloadBytes:     14,
			MaxAnnotationsBytes: 14,
			MaxRequestBodyBytes: 4096,
		}
		client, storageDB, _ := fileDraftHTTP(t, database, nil, limits)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "file-limits", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		largeJSON := draftReadyFile(t, storageDB)
		if err := storageDB.Model(&st.StorageFile{}).
			Where("id = ?", largeJSON).
			Updates(map[string]any{"size": 1048576, "expected_size": 1048576, "content_type": "application/json"}).
			Error; err != nil {
			t.Fatal(err)
		}
		refs := []api.FileReference{{FileID: largeJSON, Path: "agent.json"}}
		d, err := client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: ap,
				RequestKey:   "explicit-file",
				Payload:      json.RawMessage(`{"external":1}`),
				Files:        refs,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Files) != 1 || d.Files[0].Size != 1048576 || string(d.Payload) != `{"external":1}` {
			t.Fatalf("file implicitly changed payload %+v", d)
		}
		v, err := client.CreateVersion(
			ctx,
			&api.CreateVersionRequest{
				ArtifactPath:          ap,
				RequestKey:            "publish",
				DraftID:               &d.ID,
				ExpectedDraftRevision: &d.Revision,
			},
		)
		if err != nil || len(v.Files) != 1 || v.Files[0].Size != 1048576 {
			t.Fatalf("large explicit JSON %+v %v", v, err)
		}
		_, err = client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: ap,
				RequestKey:   "large-inline",
				Payload:      json.RawMessage(`{"s":"12345678"}`),
				Files:        refs,
			},
		)
		expectError(t, err, 413, api.CodePayloadTooLarge)
		second := draftReadyFile(t, storageDB)
		third := draftReadyFile(t, storageDB)
		tooMany := append(
			refs,
			api.FileReference{FileID: second, Path: "second"},
			api.FileReference{FileID: third, Path: "third"},
		)
		_, err = client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{ArtifactPath: ap, RequestKey: "draft-many", Files: tooMany},
		)
		expectError(t, err, 400, "")
		_, err = client.CreateVersion(
			ctx,
			&api.CreateVersionRequest{ArtifactPath: ap, RequestKey: "version-many", Files: tooMany},
		)
		expectError(t, err, 400, "")
		_, err = client.UpdateDraft(
			ctx,
			&api.UpdateDraftRequest{
				DraftPath:        api.DraftPath{ArtifactPath: ap, DraftID: d.ID},
				RequestKey:       "save-many",
				ExpectedRevision: &d.Revision,
				Payload:          json.RawMessage(`{}`),
				Annotations:      json.RawMessage(`{}`),
				Files:            tooMany,
			},
		)
		expectError(t, err, 400, "")
		for _, bad := range [][]api.FileReference{{refs[0], refs[0]}, {refs[0], {FileID: second, Path: refs[0].Path}}} {
			_, err = client.CreateVersion(
				ctx,
				&api.CreateVersionRequest{ArtifactPath: ap, RequestKey: "duplicate", Files: bad},
			)
			expectError(t, err, 400, "")
		}
		uploading := bindingSeed(t, storageDB, "uploading")
		if err := storageDB.Model(&st.StorageFile{}).
			Where("id = ?", uploading).
			Update("tenant_id", tenantA).
			Error; err != nil {
			t.Fatal(err)
		}
		_, err = client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: ap,
				RequestKey:   "uploading",
				Files:        []api.FileReference{{FileID: uploading, Path: "unfinished"}},
			},
		)
		expectError(t, err, 409, "")
		if err := storageDB.Model(&st.StorageFile{}).
			Where("id = ?", third).
			Update("scope_key", projectA).
			Error; err != nil {
			t.Fatal(err)
		}
		_, err = client.CreateDraft(
			ctx,
			&api.CreateDraftRequest{
				ArtifactPath: ap,
				RequestKey:   "wrong-scope",
				Files:        []api.FileReference{{FileID: third, Path: "private"}},
			},
		)
		expectError(t, err, 404, "")
	})
}

func TestFileDraftCopyRechecksSourceVersionAfterBind(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		for _, action := range []string{"annotations", "delete"} {
			t.Run(action, func(t *testing.T) {
				var trigger atomic.Bool
				var mutate func()
				client, storageDB, _ := fileDraftHTTP(t, database, func() {
					if trigger.Swap(false) {
						mutate()
					}
				})
				ctx := identity(t, "tenant")
				a := create(t, client, ctx, "copy-"+action, "model")
				ap := api.ArtifactPath{ArtifactID: a.ID}
				v, err := client.CreateVersion(
					ctx,
					&api.CreateVersionRequest{
						ArtifactPath: ap,
						RequestKey:   "source",
						Files: []api.FileReference{
							{FileID: draftReadyFile(t, storageDB), Path: "config.json"},
						},
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				path := api.VersionPath{ArtifactPath: ap, VersionID: v.ID}
				done := make(chan error, 1)
				mutate = func() {
					if action == "annotations" {
						_, err := client.UpdateAnnotations(
							ctx,
							&api.UpdateAnnotationsRequest{
								VersionPath:      path,
								ExpectedRevision: &v.Revision,
								Annotations:      json.RawMessage(`{"edited":true}`),
							},
						)
						done <- err
					} else {
						_, err := client.DeleteVersion(
							ctx,
							&api.DeleteVersionRequest{VersionPath: path},
						)
						done <- err
					}
				}
				trigger.Store(true)
				_, err = client.CreateDraft(
					ctx,
					&api.CreateDraftRequest{
						ArtifactPath:    ap,
						RequestKey:      "copy",
						SourceVersionID: &v.ID,
					},
				)
				if mutationErr := <-done; mutationErr != nil {
					t.Fatal(mutationErr)
				}
				if action == "annotations" {
					expectError(t, err, 409, api.CodeRevisionConflict)
				} else {
					expectError(t, err, 404, "")
				}
				drafts, err := client.ListDrafts(ctx, &api.ListDraftsRequest{ArtifactPath: ap})
				if err != nil || len(drafts.Items) != 0 {
					t.Fatalf("copied stale source %+v %v", drafts, err)
				}
			})
		}
	})
}

func TestFileDraftSuccessfulReceiptsSurviveLowerLimits(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		remote := &publicationStorage{expiry: time.Now().UTC().Add(time.Hour), refs: map[string]string{}}
		limits := &service.VersionConfig{
			MaxFilesPerVersion:  2,
			MaxPayloadBytes:     100,
			MaxAnnotationsBytes: 100,
			MaxRequestBodyBytes: 4096,
		}
		client, _, handoff := publicationHTTP(t, database, remote, limits)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "lower-limits", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		createReq := &api.CreateDraftRequest{
			ArtifactPath: ap,
			RequestKey:   "create",
			Payload:      json.RawMessage(`{"original":1}`),
			Files:        []api.FileReference{{FileID: "file-1", Path: "a"}, {FileID: "file-2", Path: "b"}},
		}
		draft, err := client.CreateDraft(ctx, createReq)
		if err != nil {
			t.Fatal(err)
		}
		save := &api.UpdateDraftRequest{
			DraftPath:        api.DraftPath{ArtifactPath: ap, DraftID: draft.ID},
			RequestKey:       "save",
			ExpectedRevision: &draft.Revision,
			Payload:          json.RawMessage(`{"saved":1}`),
			Annotations:      json.RawMessage(`{}`),
			Files:            createReq.Files,
		}
		applied, err := client.UpdateDraft(ctx, save)
		if err != nil {
			t.Fatal(err)
		}
		next := *save
		next.RequestKey = "later"
		next.ExpectedRevision = &applied.Draft.Revision
		next.Payload = json.RawMessage(`{"current":2}`)
		next.Files = []api.FileReference{{FileID: "file-1", Path: "renamed-a"}, {FileID: "file-2", Path: "b"}}
		current, err := client.UpdateDraft(ctx, &next)
		if err != nil {
			t.Fatal(err)
		}
		publish := &api.CreateVersionRequest{
			ArtifactPath:          ap,
			RequestKey:            "publish",
			DraftID:               &draft.ID,
			ExpectedDraftRevision: &current.Draft.Revision,
		}
		version, err := client.CreateVersion(ctx, publish)
		if err != nil {
			t.Fatal(err)
		}
		stricter := &service.VersionConfig{
			MaxFilesPerVersion:  1,
			MaxPayloadBytes:     2,
			MaxAnnotationsBytes: 2,
			MaxRequestBodyBytes: 4096,
		}
		lower, _, _ := publicationHTTP(t, database, remote, stricter)
		replay, err := lower.CreateDraft(ctx, createReq)
		if err != nil || replay.ID != draft.ID || replay.Revision != current.Draft.Revision ||
			string(replay.Payload) != string(current.Draft.Payload) ||
			replay.Files[1].Path != current.Draft.Files[1].Path {
			t.Fatalf("create replay %+v %v", replay, err)
		}
		saved, err := lower.UpdateDraft(ctx, save)
		if err != nil || saved.AppliedRevision != applied.AppliedRevision ||
			saved.Draft.Revision != current.Draft.Revision ||
			string(saved.Draft.Payload) != string(current.Draft.Payload) ||
			saved.Draft.Files[1].Path != current.Draft.Files[1].Path {
			t.Fatalf("save replay %+v %v", saved, err)
		}
		repeated, err := lower.CreateVersion(ctx, publish)
		if err != nil || repeated.ID != version.ID {
			t.Fatalf("publish replay %+v %v", repeated, err)
		}
		newPublish := *publish
		newPublish.RequestKey = "new-publish"
		_, err = lower.CreateVersion(ctx, &newPublish)
		if err == nil {
			t.Fatal("new publication ignored current limits")
		}
		_, err = lower.CreateDraft(
			ctx,
			&api.CreateDraftRequest{ArtifactPath: ap, RequestKey: "new-copy", SourceVersionID: &version.ID},
		)
		if err == nil {
			t.Fatal("source copy ignored current limits")
		}
		// Committed holdings do not expire with their publication attempt deadline.
		if err := database.Model(&table.ReferenceChange{}).
			Where("artifact_id = ? AND state = ?", a.ID, "committed").
			Update("deadline", time.Now().Add(-time.Hour)).
			Error; err != nil {
			t.Fatal(err)
		}
		worker, err := backgroundtask.New(database, nil)
		if err != nil {
			t.Fatal(err)
		}
		worker.WithHandoff(handoff)
		if err := worker.Scan(t.Context()); err != nil {
			t.Fatal(err)
		}
		runReferenceTasks(t, database, handoff)
		var committed int64
		if err := database.Model(&table.ReferenceChange{}).
			Where("artifact_id = ? AND state = ?", a.ID, "committed").
			Count(&committed).
			Error; err != nil ||
			committed != 4 {
			t.Fatalf("committed decisions changed %d %v", committed, err)
		}
	})
}
