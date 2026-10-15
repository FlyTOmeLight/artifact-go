package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"

	"github.com/FlyTOmeLight/artifact-go/internal/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	handoff "github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
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
	server *httptest.Server,
	token, operation string,
	refs []handoff.Reference,
) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(
		handoff.ReferencesRequest{Scope: handoff.Scope{TenantID: "tenant-1"}, References: refs},
	)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/internal/v1/storage/references/"+operation,
		bytes.NewReader(raw),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, body
}

func bindingExpect(
	t *testing.T,
	server *httptest.Server,
	token, op string,
	refs []handoff.Reference,
	status int,
	code string,
) []byte {
	t.Helper()
	got, body := bindingRequest(t, server, token, op, refs)
	if got != status || (code != "" && !bytes.Contains(body, []byte(code))) {
		t.Fatalf("%s: status=%d body=%s", op, got, body)
	}
	return body
}

func bindingStatuses(
	t *testing.T,
	server *httptest.Server,
	token, op string,
	refs []handoff.Reference,
) []handoff.Status {
	t.Helper()
	raw := bindingExpect(t, server, token, op, refs, 200, "")
	var rows []handoff.Status
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func bindingServer(t *testing.T, database *db.DB) *httptest.Server {
	t.Helper()
	if err := st.Migrate(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	handler, err := handoff.NewHandler(storage.NewBindingService(database), []handoff.Consumer{
		{ID: "artifact", Tokens: []string{"first", "rotated"}, Scopes: []handoff.Scope{{TenantID: "tenant-1"}}},
		{ID: "other", Tokens: []string{"second"}, Scopes: []handoff.Scope{{TenantID: "tenant-1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestStorageBindingLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := bindingServer(t, database)
		file := bindingSeed(t, database, "ready")
		ref := handoff.Reference{ReferenceID: uuid.NewString(), FileID: file}
		statuses := bindingStatuses(t, server, "first", "query", []handoff.Reference{ref})
		if statuses[0].State != "absent" || statuses[0].ReleaseComplete ||
			statuses[0].ReleaseRequiresDeletion != nil {
			t.Fatal(statuses)
		}
		bindingExpect(t, server, "first", "bind", []handoff.Reference{ref}, 200, "")
		bindingExpect(t, server, "rotated", "bind", []handoff.Reference{ref}, 200, "")
		// A second consumer's same reference identifier is a separate holding.
		bindingExpect(t, server, "second", "bind", []handoff.Reference{ref}, 200, "")
		statuses = bindingStatuses(t, server, "first", "release", []handoff.Reference{ref})
		if !statuses[0].ReleaseComplete || statuses[0].ReleaseRequiresDeletion == nil ||
			*statuses[0].ReleaseRequiresDeletion {
			t.Fatal(statuses)
		}
		bindingExpect(t, server, "first", "bind", []handoff.Reference{ref}, 409, "ReferenceReleased")
		statuses = bindingStatuses(t, server, "second", "release", []handoff.Reference{ref})
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

		for _, token := range []string{"first", "second", "rotated"} {
			statuses = bindingStatuses(t, server, token, "query", []handoff.Reference{ref})
			if !statuses[0].ReleaseComplete {
				t.Fatal(statuses)
			}
		}
		statuses = bindingStatuses(t, server, "second", "release", []handoff.Reference{ref})
		if !statuses[0].ReleaseComplete {
			t.Fatal(statuses)
		}
		missingRef := handoff.Reference{ReferenceID: uuid.NewString(), FileID: file}
		statuses = bindingStatuses(t, server, "first", "release", []handoff.Reference{missingRef})
		if !statuses[0].ReleaseComplete || *statuses[0].ReleaseRequiresDeletion {
			t.Fatal(statuses)
		}
	})
}

func TestStorageBindingAtomicityAndTombstones(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := bindingServer(t, database)
		ready := bindingSeed(t, database, "ready")
		uploading := bindingSeed(t, database, "uploading")
		refs := []handoff.Reference{
			{ReferenceID: uuid.NewString(), FileID: ready},
			{ReferenceID: uuid.NewString(), FileID: uploading},
		}
		bindingExpect(t, server, "first", "bind", refs, 409, "FileNotBindable")
		for _, s := range bindingStatuses(t, server, "first", "query", refs) {
			if s.State != "absent" {
				t.Fatal(s)
			}
		}
		// A never-bound release must not expire or delete preparation files.
		for _, s := range bindingStatuses(t, server, "first", "release", refs) {
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
		bindingExpect(t, server, "first", "bind", refs[:1], 409, "ReferenceReleased")
		conflict := []handoff.Reference{{ReferenceID: refs[0].ReferenceID, FileID: uploading}}
		bindingExpect(t, server, "first", "release", conflict, 409, "ReferenceConflict")
		absentFile := []handoff.Reference{{ReferenceID: uuid.NewString(), FileID: uuid.NewString()}}
		bindingExpect(t, server, "first", "query", absentFile, 404, "FileNotFound")
		// Releasing a valid active item and a conflicting item is all-or-nothing.
		active := handoff.Reference{ReferenceID: uuid.NewString(), FileID: ready}
		bindingExpect(t, server, "first", "bind", []handoff.Reference{active}, 200, "")
		bindingExpect(
			t,
			server,
			"first",
			"release",
			[]handoff.Reference{active, conflict[0]},
			409,
			"ReferenceConflict",
		)
		if rows := bindingStatuses(
			t,
			server,
			"first",
			"query",
			[]handoff.Reference{active},
		); rows[0].State != "active" {
			t.Fatal(rows)
		}
	})
}

func TestStorageBindingConcurrentFinalRelease(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := bindingServer(t, database)
		for iteration := 0; iteration < 8; iteration++ {
			file := bindingSeed(t, database, "ready")
			old := handoff.Reference{ReferenceID: uuid.NewString(), FileID: file}
			fresh := handoff.Reference{ReferenceID: uuid.NewString(), FileID: file}
			bindingExpect(t, server, "first", "bind", []handoff.Reference{old}, 200, "")
			var wg sync.WaitGroup
			start := make(chan struct{})
			statuses := make(chan int, 2)
			for _, call := range []struct {
				op  string
				ref handoff.Reference
			}{{op: "release", ref: old}, {op: "bind", ref: fresh}} {
				wg.Go(func() {
					<-start
					status, _ := bindingRequest(
						t,
						server,
						"first",
						call.op,
						[]handoff.Reference{call.ref},
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
			rows := bindingStatuses(t, server, "first", "query", []handoff.Reference{fresh})
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
		server := bindingServer(t, database)
		connection, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		defer func() { database.Statement.ConnPool = original }()
		for _, persist := range []bool{false, true} {
			file := bindingSeed(t, database, "ready")
			ref := handoff.Reference{ReferenceID: uuid.NewString(), FileID: file}
			pool := &uncertainCommitPool{ConnPool: connection, connection: connection, persist: persist}
			database.Statement.ConnPool = pool
			bindingExpect(
				t,
				server,
				"first",
				"bind",
				[]handoff.Reference{ref},
				503,
				"StorageOutcomeUnknown",
			)
			database.Statement.ConnPool = original
			rows := bindingStatuses(t, server, "first", "query", []handoff.Reference{ref})
			if (rows[0].State == "active") != persist {
				t.Fatal(rows)
			}
			bindingExpect(t, server, "first", "bind", []handoff.Reference{ref}, 200, "")
			pool = &uncertainCommitPool{ConnPool: connection, connection: connection, persist: persist}
			database.Statement.ConnPool = pool
			bindingExpect(
				t,
				server,
				"first",
				"release",
				[]handoff.Reference{ref},
				503,
				"StorageOutcomeUnknown",
			)
			database.Statement.ConnPool = original
			rows = bindingStatuses(t, server, "first", "query", []handoff.Reference{ref})
			if (rows[0].State == "released") != persist {
				t.Fatal(rows)
			}
			bindingExpect(t, server, "first", "release", []handoff.Reference{ref}, 200, "")
		}
	})
}

func TestStorageBindingExpiryAndScope(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := bindingServer(t, database)
		file := bindingSeed(t, database, "ready")
		ref := handoff.Reference{ReferenceID: uuid.NewString(), FileID: file}
		bindingExpect(t, server, "first", "bind", []handoff.Reference{ref}, 200, "")
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", file).
			Update("expires_at", time.Now().Add(-time.Hour)).
			Error; err != nil {
			t.Fatal(err)
		}
		// Existing references have no preparation TTL; new consumers can reuse bound files.
		bindingExpect(t, server, "second", "bind", []handoff.Reference{ref}, 200, "")
		expired := bindingSeed(t, database, "ready")
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", expired).
			Update("expires_at", time.Now().Add(-time.Hour)).
			Error; err != nil {
			t.Fatal(err)
		}
		bindingExpect(
			t,
			server,
			"first",
			"bind",
			[]handoff.Reference{{ReferenceID: uuid.NewString(), FileID: expired}},
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
				server,
				"first",
				op,
				[]handoff.Reference{{ReferenceID: uuid.NewString(), FileID: foreign}},
				404,
				"FileNotFound",
			)
		}
		// Metadata comes from the primary storage record and retains the original deadline.
		raw, _ := json.Marshal(
			handoff.MetadataRequest{
				Scope:   handoff.Scope{TenantID: "tenant-1"},
				FileIDs: []string{file, expired},
			},
		)
		req, _ := http.NewRequest(
			http.MethodPost,
			server.URL+"/internal/v1/storage/reference-files/query",
			bytes.NewReader(raw),
		)
		req.Header.Set("Authorization", "Bearer first")
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal(res.Status)
		}
		var files []handoff.FileMetadata
		if err = json.NewDecoder(res.Body).Decode(&files); err != nil {
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
		server := bindingServer(t, database)
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
						server,
						"first",
						"bind",
						[]handoff.Reference{{ReferenceID: id, FileID: file}},
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
		server := bindingServer(t, database)
		file := bindingSeed(t, database, "ready")
		ref := handoff.Reference{ReferenceID: uuid.NewString(), FileID: file}
		deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", file).
			Updates(map[string]any{"last_grant_expires_at": deadline, "backend_id": "local"}).
			Error; err != nil {
			t.Fatal(err)
		}
		bindingExpect(t, server, "first", "bind", []handoff.Reference{ref}, 200, "")
		bindingExpect(t, server, "first", "release", []handoff.Reference{ref}, 200, "")
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
