package integration_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	versionapp "github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestDraftReferenceRecovery(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		p := &publicationStorage{expiry: time.Now().Add(-time.Hour), refs: map[string]string{}}
		p.deleted.Store(true)
		client, _, references := publicationHTTP(t, database, p)
		a := create(t, client, identity(t, "tenant"), "draft-recovery", "model")
		for _, kind := range []string{"create_draft", "update_draft"} {
			for _, hasFiles := range []bool{false, true} {
				key := fmt.Sprintf("%s-%t", kind, hasFiles)
				plan := versionapp.ReferencePlan{Scope: storage.Scope{TenantID: tenantA}}
				if hasFiles {
					plan.References = []versionapp.PlannedReference{
						{
							Reference: storage.Reference{
								ReferenceID: key,
								FileID:      "file-one",
							},
							OriginalExpiresAt: p.expiry,
						},
					}
					p.refs[key] = "active"
				}
				raw, err := json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				row := table.ReferenceChange{
					ArtifactID:    a.ID,
					ActorID:       "user-a",
					OperationKind: kind,
					RequestKey:    key,
					RequestHash:   "fixed",
					TenantID:      tenantA,
					ScopeKey:      "tenant",
					AttemptID:     key,
					State:         "preparing",
					Deadline:      time.Now().Add(-time.Minute),
					Plan:          raw,
				}
				if err = database.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		worker, err := backgroundtask.New(database, nil)
		if err != nil {
			t.Fatal(err)
		}
		worker.WithReferences(references)
		if err = worker.Scan(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err = worker.Scan(t.Context()); err != nil {
			t.Fatal(err)
		}
		var changes []table.ReferenceChange
		if err = database.Where("artifact_id = ?", a.ID).Find(&changes).Error; err != nil {
			t.Fatal(err)
		}
		if len(changes) != 4 {
			t.Fatalf("decisions=%d", len(changes))
		}
		for _, row := range changes {
			if row.State != "aborted" {
				t.Fatalf("%s state=%s", row.RequestKey, row.State)
			}
		}
		var tasks []table.BackgroundTask
		if err = database.Where("kind = ?", "reference-release").Find(&tasks).Error; err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 2 {
			t.Fatalf("release tasks=%d; empty plans must not create tasks", len(tasks))
		}
		for range 2 {
			task, e := worker.Claim(t.Context())
			if e != nil || task == nil {
				t.Fatalf("claim: %v", e)
			}
			if e = worker.Execute(t.Context(), task); e != nil {
				t.Fatal(e)
			}
		}
		if p.releaseCalls.Load() != 2 {
			t.Fatalf("release calls=%d", p.releaseCalls.Load())
		}
		for _, model := range []any{&table.Draft{}, &table.Version{}} {
			var count int64
			if err = database.Model(model).Where("artifact_id = ?", a.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("recovery committed a business snapshot")
			}
		}
		var stored table.Artifact
		if err = database.First(&stored, "id = ?", a.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.LastSequence != 0 {
			t.Fatal("recovery advanced version sequence")
		}
		var failed int64
		if err = database.Model(&table.BackgroundTask{}).
			Where("state = ?", "failed").
			Count(&failed).
			Error; err != nil {
			t.Fatal(err)
		}
		if failed != 0 {
			t.Fatal("recovery created a failed task")
		}
	})
}
