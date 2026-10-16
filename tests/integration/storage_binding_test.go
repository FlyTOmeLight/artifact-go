package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/google/uuid"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func bindingSeed(t *testing.T, database *db.DB, state string) string {
	t.Helper()
	id := uuid.NewString()
	size := int64(7)
	row := st.StorageFile{
		ID:           id,
		TenantID:     "tenant-1",
		ScopeKey:     "tenant",
		CreatedBy:    "u",
		RequestKey:   id,
		RequestHash:  id,
		BackendID:    "fixture",
		Bucket:       "bucket",
		ObjectKey:    id,
		ExpectedSize: 7,
		Size:         &size,
		ContentType:  "text/plain",
		State:        state,
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	}
	if state == "uploading" {
		row.Size = nil
	}
	if err := database.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func bindingRequest(
	t *testing.T,
	services map[string]storage.ReferenceService,
	consumer, operation string,
	refs []storage.Reference,
) (int, []byte) {
	t.Helper()
	var result any
	var err error
	svc := services[consumer]
	scope := storage.Scope{TenantID: "tenant-1"}
	switch operation {
	case "bind":
		result, err = svc.Bind(t.Context(), scope, refs)
	case "release":
		result, err = svc.Release(t.Context(), scope, refs)
	case "query":
		result, err = svc.Statuses(t.Context(), scope, refs)
	default:
		t.Fatalf("invalid operation %s", operation)
	}
	if err != nil {
		var detail *serrors.Error
		if !errors.As(err, &detail) {
			t.Fatal(err)
		}
		return detail.Status, []byte(detail.Code)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return 200, raw
}

func bindingExpect(
	t *testing.T,
	services map[string]storage.ReferenceService,
	consumer, op string,
	refs []storage.Reference,
	status int,
	code string,
) []byte {
	t.Helper()
	got, body := bindingRequest(t, services, consumer, op, refs)
	if got != status || (code != "" && !bytes.Contains(body, []byte(code))) {
		t.Fatalf("%s: status=%d body=%s", op, got, body)
	}
	return body
}

func bindingStatuses(
	t *testing.T,
	services map[string]storage.ReferenceService,
	consumer, op string,
	refs []storage.Reference,
) []storage.Status {
	t.Helper()
	raw := bindingExpect(t, services, consumer, op, refs, 200, "")
	var rows []storage.Status
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func bindingServices(t *testing.T, database *db.DB) map[string]storage.ReferenceService {
	t.Helper()
	if err := st.Migrate(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	primary := storage.NewBindingService(database, "artifact")
	return map[string]storage.ReferenceService{
		"first":     primary,
		"restarted": storage.NewBindingService(database, "artifact"),
		"second":    storage.NewBindingService(database, "other"),
	}
}

func TestStorageBindingLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		services := bindingServices(t, database)
		file := bindingSeed(t, database, "ready")
		ref := storage.Reference{ReferenceID: uuid.NewString(), FileID: file}
		statuses := bindingStatuses(t, services, "first", "query", []storage.Reference{ref})
		if statuses[0].State != "absent" || statuses[0].ReleaseComplete ||
			statuses[0].ReleaseRequiresDeletion != nil {
			t.Fatal(statuses)
		}
		bindingExpect(t, services, "first", "bind", []storage.Reference{ref}, 200, "")
		bindingExpect(t, services, "restarted", "bind", []storage.Reference{ref}, 200, "")
		// A second consumer's same reference identifier is a separate holding.
		bindingExpect(t, services, "second", "bind", []storage.Reference{ref}, 200, "")
		statuses = bindingStatuses(t, services, "first", "release", []storage.Reference{ref})
		if !statuses[0].ReleaseComplete || statuses[0].ReleaseRequiresDeletion == nil ||
			*statuses[0].ReleaseRequiresDeletion {
			t.Fatal(statuses)
		}
		bindingExpect(t, services, "first", "bind", []storage.Reference{ref}, 409, "ReferenceReleased")
		statuses = bindingStatuses(t, services, "second", "release", []storage.Reference{ref})
		if statuses[0].ReleaseComplete || statuses[0].ReleaseRequiresDeletion == nil ||
			!*statuses[0].ReleaseRequiresDeletion {
			t.Fatal(statuses)
		}
		var task st.StorageTask
		if err := database.Where("target_id = ?", file).First(&task).Error; err != nil {
			t.Fatal(err)
		}
		var row st.StorageFile
		if err := database.First(&row, "id = ?", file).Error; err != nil {
			t.Fatal(err)
		}
		if row.State != "deleting" {
			t.Fatal(row.State)
		}
		// The real storage worker commits its first object deletion receipt.
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", file).
			Update("backend_id", "local").
			Error; err != nil {
			t.Fatal(err)
		}
		objects := &gcObjects{exists: true}
		worker := gcWorker(t, database, objects)
		claim, err := worker.Claim(t.Context())
		if err != nil || claim == nil {
			t.Fatalf("claim %v %v", claim, err)
		}
		if err = worker.Execute(t.Context(), claim); err != nil {
			t.Fatal(err)
		}

		for _, consumer := range []string{"first", "second", "restarted"} {
			statuses = bindingStatuses(t, services, consumer, "query", []storage.Reference{ref})
			if !statuses[0].ReleaseComplete {
				t.Fatal(statuses)
			}
		}
		statuses = bindingStatuses(t, services, "second", "release", []storage.Reference{ref})
		if !statuses[0].ReleaseComplete {
			t.Fatal(statuses)
		}
		missingRef := storage.Reference{ReferenceID: uuid.NewString(), FileID: file}
		statuses = bindingStatuses(t, services, "first", "release", []storage.Reference{missingRef})
		if !statuses[0].ReleaseComplete || *statuses[0].ReleaseRequiresDeletion {
			t.Fatal(statuses)
		}
	})
}

func TestStorageBindingAtomicityAndTombstones(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		services := bindingServices(t, database)
		ready := bindingSeed(t, database, "ready")
		uploading := bindingSeed(t, database, "uploading")
		refs := []storage.Reference{
			{ReferenceID: uuid.NewString(), FileID: ready},
			{ReferenceID: uuid.NewString(), FileID: uploading},
		}
		bindingExpect(t, services, "first", "bind", refs, 409, "FileNotBindable")
		for _, s := range bindingStatuses(t, services, "first", "query", refs) {
			if s.State != "absent" {
				t.Fatal(s)
			}
		}
		// A never-bound release must not expire or delete preparation files.
		for _, s := range bindingStatuses(t, services, "first", "release", refs) {
			if !s.ReleaseComplete || s.ReleaseRequiresDeletion == nil || *s.ReleaseRequiresDeletion {
				t.Fatal(s)
			}
		}
		for _, id := range []string{ready, uploading} {
			var row st.StorageFile
			if err := database.First(&row, "id = ?", id).Error; err != nil {
				t.Fatal(err)
			}
			if row.State == "deleting" {
				t.Fatal("unknown release deleted preparation")
			}
		}
		bindingExpect(t, services, "first", "bind", refs[:1], 409, "ReferenceReleased")
		conflict := []storage.Reference{{ReferenceID: refs[0].ReferenceID, FileID: uploading}}
		bindingExpect(t, services, "first", "release", conflict, 409, "ReferenceConflict")
		absentFile := []storage.Reference{{ReferenceID: uuid.NewString(), FileID: uuid.NewString()}}
		bindingExpect(t, services, "first", "query", absentFile, 404, "FileNotFound")
		// Releasing a valid active item and a conflicting item is all-or-nothing.
		active := storage.Reference{ReferenceID: uuid.NewString(), FileID: ready}
		bindingExpect(t, services, "first", "bind", []storage.Reference{active}, 200, "")
		bindingExpect(
			t,
			services,
			"first",
			"release",
			[]storage.Reference{active, conflict[0]},
			409,
			"ReferenceConflict",
		)
		if rows := bindingStatuses(
			t,
			services,
			"first",
			"query",
			[]storage.Reference{active},
		); rows[0].State != "active" {
			t.Fatal(rows)
		}
	})
}

func TestStorageBindingConcurrentFinalRelease(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		services := bindingServices(t, database)
		for iteration := 0; iteration < 8; iteration++ {
			file := bindingSeed(t, database, "ready")
			old := storage.Reference{ReferenceID: uuid.NewString(), FileID: file}
			fresh := storage.Reference{ReferenceID: uuid.NewString(), FileID: file}
			bindingExpect(t, services, "first", "bind", []storage.Reference{old}, 200, "")
			var wg sync.WaitGroup
			start := make(chan struct{})
			statuses := make(chan int, 2)
			for _, call := range []struct {
				op  string
				ref storage.Reference
			}{{op: "release", ref: old}, {op: "bind", ref: fresh}} {
				wg.Go(func() {
					<-start
					status, _ := bindingRequest(
						t,
						services,
						"first",
						call.op,
						[]storage.Reference{call.ref},
					)
					statuses <- status
				})
			}
			close(start)
			wg.Wait()
			close(statuses)
			for status := range statuses {
				if status != 200 && status != 409 {
					t.Fatal(status)
				}
			}
			rows := bindingStatuses(t, services, "first", "query", []storage.Reference{fresh})
			var fileRow st.StorageFile
			if err := database.First(&fileRow, "id = ?", file).Error; err != nil {
				t.Fatal(err)
			}
			if (rows[0].State == "active") != (fileRow.State == "bound") {
				t.Fatal(fmt.Sprintf("inconsistent race: %+v %s", rows, fileRow.State))
			}
		}
	})
}

func TestStorageBindingUnknownCommit(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		services := bindingServices(t, database)
		connection, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		defer func() { database.Statement.ConnPool = original }()
		for _, persist := range []bool{false, true} {
			file := bindingSeed(t, database, "ready")
			ref := storage.Reference{ReferenceID: uuid.NewString(), FileID: file}
			pool := &uncertainCommitPool{ConnPool: connection, connection: connection, persist: persist}
			database.Statement.ConnPool = pool
			bindingExpect(
				t,
				services,
				"first",
				"bind",
				[]storage.Reference{ref},
				503,
				"StorageOutcomeUnknown",
			)
			database.Statement.ConnPool = original
			rows := bindingStatuses(t, services, "first", "query", []storage.Reference{ref})
			if (rows[0].State == "active") != persist {
				t.Fatal(rows)
			}
			bindingExpect(t, services, "first", "bind", []storage.Reference{ref}, 200, "")
			pool = &uncertainCommitPool{ConnPool: connection, connection: connection, persist: persist}
			database.Statement.ConnPool = pool
			bindingExpect(
				t,
				services,
				"first",
				"release",
				[]storage.Reference{ref},
				503,
				"StorageOutcomeUnknown",
			)
			database.Statement.ConnPool = original
			rows = bindingStatuses(t, services, "first", "query", []storage.Reference{ref})
			if (rows[0].State == "released") != persist {
				t.Fatal(rows)
			}
			bindingExpect(t, services, "first", "release", []storage.Reference{ref}, 200, "")
		}
	})
}

func TestStorageBindingExpiryAndScope(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		services := bindingServices(t, database)
		file := bindingSeed(t, database, "ready")
		ref := storage.Reference{ReferenceID: uuid.NewString(), FileID: file}
		bindingExpect(t, services, "first", "bind", []storage.Reference{ref}, 200, "")
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", file).
			Update("expires_at", time.Now().Add(-time.Hour)).
			Error; err != nil {
			t.Fatal(err)
		}
		// Existing references have no preparation TTL; new consumers can reuse bound files.
		bindingExpect(t, services, "second", "bind", []storage.Reference{ref}, 200, "")
		expired := bindingSeed(t, database, "ready")
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", expired).
			Update("expires_at", time.Now().Add(-time.Hour)).
			Error; err != nil {
			t.Fatal(err)
		}
		bindingExpect(
			t,
			services,
			"first",
			"bind",
			[]storage.Reference{{ReferenceID: uuid.NewString(), FileID: expired}},
			409,
			"FileNotBindable",
		)
		foreign := bindingSeed(t, database, "ready")
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", foreign).
			Update("scope_key", "project:other").
			Error; err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"bind", "release", "query"} {
			bindingExpect(
				t,
				services,
				"first",
				op,
				[]storage.Reference{{ReferenceID: uuid.NewString(), FileID: foreign}},
				404,
				"FileNotFound",
			)
		}
		for _, target := range []storage.Scope{{TenantID: "other-tenant"}, {TenantID: "tenant-1", ProjectID: func() *string { v := "other-project"; return &v }()}} {
			svc := services["first"]
			for _, operation := range []func() error{
				func() error { _, err := svc.Metadata(t.Context(), target, []string{file}); return err },
				func() error { _, err := svc.Bind(t.Context(), target, []storage.Reference{ref}); return err },
				func() error { _, err := svc.Statuses(t.Context(), target, []storage.Reference{ref}); return err },
				func() error { _, err := svc.Release(t.Context(), target, []storage.Reference{ref}); return err },
			} {
				var detail *serrors.Error
				if err := operation(); !errors.As(err, &detail) || detail.Status != 404 {
					t.Fatalf("cross-scope reference access: %v", err)
				}
			}
		}

		// Metadata comes from the primary storage record and retains the original deadline.
		files, err := services["first"].Metadata(
			t.Context(),
			storage.Scope{TenantID: "tenant-1"},
			[]string{file, expired},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 2 || files[0].FileID != file || files[0].State != "bound" ||
			files[0].OriginalExpiresAt.After(time.Now()) {
			t.Fatal(files)
		}
	})
}

func TestStorageBindingReferenceIdentityRace(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		services := bindingServices(t, database)
		for iteration := 0; iteration < 8; iteration++ {
			first := bindingSeed(t, database, "ready")
			second := bindingSeed(t, database, "ready")
			id := uuid.NewString()
			var wg sync.WaitGroup
			start := make(chan struct{})
			out := make(chan int, 2)
			for _, file := range []string{first, second} {
				wg.Go(func() {
					<-start
					status, body := bindingRequest(
						t,
						services,
						"first",
						"bind",
						[]storage.Reference{{ReferenceID: id, FileID: file}},
					)
					if status == 409 && !bytes.Contains(body, []byte("ReferenceConflict")) {
						t.Errorf("wrong conflict: %s", body)
					}
					out <- status
				})
			}
			close(start)
			wg.Wait()
			close(out)
			counts := map[int]int{}
			for status := range out {
				counts[status]++
			}
			if counts[200] != 1 || counts[409] != 1 {
				t.Fatal(counts)
			}
		}
	})
}

func TestStorageBindingReleaseWaitsForWriteGrant(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		services := bindingServices(t, database)
		file := bindingSeed(t, database, "ready")
		ref := storage.Reference{ReferenceID: uuid.NewString(), FileID: file}
		deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", file).
			Updates(map[string]any{"last_grant_expires_at": deadline, "backend_id": "local"}).
			Error; err != nil {
			t.Fatal(err)
		}
		bindingExpect(t, services, "first", "bind", []storage.Reference{ref}, 200, "")
		bindingExpect(t, services, "first", "release", []storage.Reference{ref}, 200, "")
		var task st.StorageTask
		if err := database.Where("target_id = ?", file).First(&task).Error; err != nil {
			t.Fatal(err)
		}
		if !task.NotBefore.Equal(deadline) {
			t.Fatalf("grant deadline lost: %s != %s", task.NotBefore, deadline)
		}
		worker := gcWorker(t, database, &gcObjects{exists: true})
		claim, err := worker.Claim(t.Context())
		if err != nil || claim != nil {
			t.Fatalf("deleted before write grant expiry: %+v %v", claim, err)
		}
	})
}
