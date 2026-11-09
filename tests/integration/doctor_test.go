package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/doctor"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestDoctorStorageChecks(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		if err := st.Migrate(t.Context(), database); err != nil {
			t.Fatal(err)
		}
		now := doctorNow(t, database)
		past, future := now.Add(-time.Hour), now.Add(time.Hour)
		for _, test := range []struct {
			id, state, reference, task string
			sweep                      *time.Time
		}{
			{id: "bound-active", state: "bound", reference: "active"},
			{id: "bound-released", state: "bound", reference: "released"},
			{id: "bound-orphan", state: "bound"},
			{id: "ready", state: "ready"},
			{id: "deleted-missing", state: "deleted"},
			{id: "deleted-missing-scheduled", state: "deleted", sweep: &future},
			{id: "deleted-done-unscheduled", state: "deleted", task: "done"},
			{id: "deleted-done-due", state: "deleted", task: "done", sweep: &past},
			{id: "deleted-done-future", state: "deleted", task: "done", sweep: &future},
			{id: "deleted-pending", state: "deleted", task: "pending"},
			{id: "deleted-running", state: "deleted", task: "running"},
			{id: "deleted-failed", state: "deleted", task: "failed", sweep: &past},
			{id: "deleted-unknown", state: "deleted", task: "unknown", sweep: &future},
		} {
			file := doctorStorageFile(test.id, test.state, now)
			file.NextSweepAt = test.sweep
			doctorCreate(t, database, &file)
			if test.reference != "" {
				doctorCreate(t, database, &st.StorageReference{
					ConsumerID: "doctor", ReferenceID: uuid.NewString(), FileID: file.ID,
					TenantID: file.TenantID, ScopeKey: file.ScopeKey, State: test.reference,
				})
			}
			if test.task != "" {
				doctorCreate(t, database, &st.StorageTask{
					ID: uuid.NewString(), Kind: "delete", TargetID: file.ID,
					Payload: []byte("{}"), State: test.task, NextRunAt: now, NotBefore: now,
					RecoveryEvents: []byte("[]"), Revision: 1, RunGeneration: 1,
				})
			}
		}
		before := doctorSnapshot(t, database, "storage_files", "storage_references", "storage_tasks")
		report, err := doctor.Run(t.Context(), database, "storage", 100)
		if err != nil {
			t.Fatal(err)
		}
		if !report.Complete || report.Errors != 6 || report.Warnings != 1 ||
			report.DatabaseTime.Before(now) || report.DatabaseTime.After(doctorNow(t, database)) {
			t.Fatalf("unexpected report: %+v", report)
		}
		doctorFindings(t, report, "bound_file_active_reference", map[string]string{
			"bound-released": "error", "bound-orphan": "error",
		})
		doctorFindings(t, report, "deleted_file_cleanup", map[string]string{
			"deleted-missing": "error", "deleted-missing-scheduled": "error",
			"deleted-done-unscheduled": "error", "deleted-unknown": "error",
			"deleted-failed": "warning",
		})
		if before != doctorSnapshot(t, database, "storage_files", "storage_references", "storage_tasks") {
			t.Fatal("doctor changed storage state")
		}
		truncated, err := doctor.Run(t.Context(), database, "storage", 1)
		if err != nil {
			t.Fatal(err)
		}
		if truncated.Complete {
			t.Fatal("limited scan claimed completeness")
		}
		for _, check := range truncated.Checks {
			if !check.Truncated || check.Scanned != 1 || len(check.Findings) > 1 {
				t.Fatalf("unbounded check: %+v", check)
			}
		}
	})
}

func TestDoctorArtifactChecks(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		if err := table.Migrate(t.Context(), database); err != nil {
			t.Fatal(err)
		}
		now := doctorNow(t, database)
		doctorCreate(t, database, &table.Artifact{
			ID: "artifact", TenantID: "tenant", ScopeKey: "scope",
			Type: "document", Name: "doctor", State: "active", VersionScheme: "sequence",
		})
		for index, test := range []struct {
			id, taskState, kind, payloadVersion string
		}{
			{id: "missing"},
			{id: "pending", taskState: "pending", kind: "reference-release", payloadVersion: "pending"},
			{id: "running", taskState: "running", kind: "reference-release", payloadVersion: "running"},
			{id: "failed", taskState: "failed", kind: "reference-release", payloadVersion: "failed"},
			{id: "done", taskState: "done", kind: "reference-release", payloadVersion: "done"},
			{id: "wrong-kind", taskState: "pending", kind: "retention", payloadVersion: "wrong-kind"},
			{id: "wrong-payload", taskState: "pending", kind: "reference-release", payloadVersion: "other"},
		} {
			doctorCreate(t, database, &table.Version{
				ID: test.id, ArtifactID: "artifact", Sequence: int64(index + 1), Number: test.id,
				Payload: []byte("{}"), Annotations: []byte("{}"), State: "deleting", PublishedAt: now,
			})
			if test.taskState != "" {
				payload, err := json.Marshal(version.ReferencePlan{
					Scope: storage.Scope{TenantID: "tenant"},
					References: []version.PlannedReference{
						{
							Reference: storage.Reference{
								ReferenceID: uuid.NewString(),
								FileID:      uuid.NewString(),
							},
						},
					},
					VersionID: test.payloadVersion,
				})
				if err != nil {
					t.Fatal(err)
				}
				task := doctorBackgroundTask(test.kind, test.taskState, uuid.NewString(), now)
				task.Payload = payload
				if test.id == "wrong-payload" {
					task.TargetID = test.id
				}
				doctorCreate(t, database, &task)
			}
		}
		for _, test := range []struct {
			id, state string
			deadline  time.Time
		}{
			{id: "expired", state: "preparing", deadline: now.Add(-time.Minute)},
			{id: "due", state: "preparing", deadline: now},
			{id: "future", state: "preparing", deadline: now.Add(time.Hour)},
			{id: "committed", state: "committed", deadline: now.Add(-time.Minute)},
			{id: "aborted", state: "aborted", deadline: now.Add(-time.Minute)},
		} {
			doctorCreate(t, database, &table.ReferenceChange{
				ArtifactID: "artifact", ActorID: "user", OperationKind: "create",
				RequestKey: test.id, AttemptID: test.id, State: test.state, Deadline: test.deadline,
				Plan: []byte("{}"), DraftID: "",
			})
		}
		retention := doctorBackgroundTask("retention", "failed", "artifact", now)
		doctorCreate(t, database, &retention)
		before := doctorSnapshot(t, database, "artifacts", "versions", "reference_changes", "background_tasks")
		report, err := doctor.Run(t.Context(), database, "artifact", 100)
		if err != nil {
			t.Fatal(err)
		}
		if !report.Complete || report.Errors != 4 || report.Warnings != 4 {
			t.Fatalf("unexpected report: %+v", report)
		}
		doctorFindings(t, report, "deleting_version_reference_release", map[string]string{
			"missing": "error", "done": "error", "wrong-kind": "error", "wrong-payload": "error",
			"failed": "warning",
		})
		doctorFindings(t, report, "expired_preparing_reference_change", map[string]string{
			"expired": "warning", "due": "warning",
		})
		doctorFindings(t, report, "failed_retention_evaluation", map[string]string{"artifact": "warning"})
		if before != doctorSnapshot(
			t,
			database,
			"artifacts",
			"versions",
			"reference_changes",
			"background_tasks",
		) {
			t.Fatal("doctor changed artifact state")
		}
		truncated, err := doctor.Run(t.Context(), database, "artifact", 1)
		if err != nil {
			t.Fatal(err)
		}
		if truncated.Complete || !truncated.Checks[0].Truncated || !truncated.Checks[1].Truncated ||
			truncated.Checks[2].Truncated {
			t.Fatalf("incorrect truncation: %+v", truncated)
		}
		for _, check := range truncated.Checks {
			if check.Scanned != 1 || len(check.Findings) > 1 {
				t.Fatalf("unbounded check: %+v", check)
			}
		}
		invalid := doctorBackgroundTask("reference-release", "pending", uuid.NewString(), now)
		invalid.Payload = []byte("{")
		doctorCreate(t, database, &invalid)
		for _, state := range []string{"pending", "running", "failed"} {
			for _, test := range []struct {
				name, payload string
			}{
				{name: "invalid-json", payload: "{"},
				{name: "missing-references", payload: `{"versionID":"pending"}`},
				{name: "null-references", payload: `{"versionID":"pending","references":null}`},
				{name: "empty-references", payload: `{"versionID":"pending","references":[]}`},
			} {
				t.Run(state+"/"+test.name, func(t *testing.T) {
					if err := database.Model(&invalid).
						Updates(map[string]any{"state": state, "payload": []byte(test.payload)}).
						Error; err != nil {
						t.Fatal(err)
					}
					report, err := doctor.Run(t.Context(), database, "artifact", 100)
					if err == nil || report != nil || !strings.Contains(err.Error(), invalid.ID) {
						t.Fatalf(
							"invalid payload produced an authoritative report: %+v %v",
							report,
							err,
						)
					}
				})
			}
		}
	})
}

func TestDoctorSnapshotAndReadOnly(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		if err := st.Migrate(t.Context(), database); err != nil {
			t.Fatal(err)
		}
		now := doctorNow(t, database)
		file := doctorStorageFile("snapshot", "bound", now)
		doctorCreate(t, database, &file)
		conn, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		var updateErr error
		name := "doctor_snapshot_test"
		if err = database.Callback().Row().After("gorm:row").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table != "storage_files" {
				return
			}
			once.Do(func() {
				query := "UPDATE storage_files SET state = 'deleted' WHERE id = 'snapshot'"
				_, updateErr = conn.ExecContext(t.Context(), query)
			})
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Callback().Row().Remove(name) })
		report, err := doctor.Run(t.Context(), database, "storage", 100)
		if err != nil || updateErr != nil {
			t.Fatalf("snapshot scan: %v, concurrent update: %v", err, updateErr)
		}
		doctorFindings(t, report, "bound_file_active_reference", map[string]string{"snapshot": "error"})
		doctorFindings(t, report, "deleted_file_cleanup", map[string]string{})
		var current st.StorageFile
		if err = database.First(&current, "id = ?", file.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.State != "deleted" {
			t.Fatal("concurrent update did not commit")
		}
		var writeErr error
		var attempted bool
		readOnlyName := "doctor_read_only_test"
		if err = database.Callback().Row().Before("gorm:row").Register(readOnlyName, func(tx *gorm.DB) {
			if attempted || tx.Statement.Table != "storage_files" {
				return
			}
			attempted = true
			transaction, ok := tx.Statement.ConnPool.(*sql.Tx)
			if !ok {
				tx.AddError(errors.New("doctor query did not use a transaction"))
				return
			}
			_, writeErr = transaction.ExecContext(t.Context(), "UPDATE storage_files SET state = 'ready'")
			if writeErr != nil {
				tx.AddError(writeErr)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Callback().Row().Remove(readOnlyName) })
		report, err = doctor.Run(t.Context(), database, "storage", 100)
		if !attempted || writeErr == nil || err == nil || report != nil {
			t.Fatalf(
				"database did not enforce read-only: attempted=%v write=%v run=%v",
				attempted,
				writeErr,
				err,
			)
		}
		if err = database.First(&current, "id = ?", file.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.State != "deleted" {
			t.Fatal("read-only transaction changed data")
		}
	})
}

func TestDoctorEmptyAndCanceled(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		for _, domain := range []string{"artifact", "storage"} {
			if domain == "artifact" {
				if err := table.Migrate(t.Context(), database); err != nil {
					t.Fatal(err)
				}
			} else if err := st.Migrate(t.Context(), database); err != nil {
				t.Fatal(err)
			}
			report, err := doctor.Run(t.Context(), database, domain, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Complete || report.Errors != 0 || report.Warnings != 0 {
				t.Fatalf("empty database report: %+v", report)
			}
			for _, check := range report.Checks {
				if check.Scanned != 0 || check.Truncated || check.Findings == nil ||
					len(check.Findings) != 0 {
					t.Fatalf("empty check: %+v", check)
				}
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		report, err := doctor.Run(ctx, database, "storage", 1)
		if !errors.Is(err, context.Canceled) || report != nil {
			t.Fatalf("canceled doctor: %+v %v", report, err)
		}
	})
}

func doctorStorageFile(id, state string, now time.Time) st.StorageFile {
	return st.StorageFile{
		ID: id, TenantID: "tenant", ScopeKey: "scope", CreatedBy: "user", RequestKey: id,
		RequestHash: "hash", BackendID: "backend", Bucket: "bucket", ObjectKey: id,
		ExpectedSize: 1, ContentType: "text/plain", State: state, ExpiresAt: now.Add(time.Hour),
	}
}

func doctorBackgroundTask(kind, state, target string, now time.Time) table.BackgroundTask {
	return table.BackgroundTask{
		ID: uuid.NewString(), Kind: kind, TargetID: target, Payload: []byte("{}"),
		State: state, NextRunAt: now, NotBefore: now, Revision: 1, RunGeneration: 1,
		RecoveryEvents: []byte("[]"),
	}
}

func doctorCreate(t *testing.T, database *db.DB, row any) {
	t.Helper()
	if err := database.Create(row).Error; err != nil {
		t.Fatal(err)
	}
}

func doctorNow(t *testing.T, database *db.DB) time.Time {
	t.Helper()
	now, err := table.DatabaseNow(database)
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func doctorFindings(t *testing.T, report *doctor.Report, name string, expected map[string]string) {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name != name {
			continue
		}
		if len(check.Findings) != len(expected) {
			t.Fatalf("%s: got %+v, expected %v", name, check.Findings, expected)
		}
		for _, finding := range check.Findings {
			if finding.Severity != expected[finding.ID] || finding.Message == "" {
				t.Fatalf("%s: unexpected finding %+v", name, finding)
			}
		}
		return
	}
	t.Fatalf("missing check %s", name)
}

func doctorSnapshot(t *testing.T, database *db.DB, names ...string) string {
	t.Helper()
	result := make(map[string][]string, len(names))
	for _, name := range names {
		var rows []map[string]any
		if err := database.Table(name).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			result[name] = append(result[name], string(raw))
		}
		sort.Strings(result[name])
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
