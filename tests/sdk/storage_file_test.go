package sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

func TestStorageFileBatchWireContract(t *testing.T) {
	var phase int
	client := storageClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tenant-ID") != "tenant" || r.Header.Get("X-Project-ID") != "project" ||
			r.Header.Get("Authorization") != "Bearer actor" {
			t.Error("missing per-request identity")
		}
		body, _ := io.ReadAll(r.Body)
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil || len(items) != 2 {
			t.Errorf("expected top-level two-item array: %s", body)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch phase {
		case 0:
			if r.Method != "POST" || r.URL.Path != "/v1/storage/files" {
				t.Errorf("route: %s %s", r.Method, r.URL.Path)
			}
			var item api.RegisterFileItem
			if err := json.Unmarshal(
				items[0],
				&item,
			); err != nil || item.Size == nil || *item.Size != 0 ||
				item.RequestKey != "one" {
				t.Errorf("registration: %s", items[0])
			}
			fmt.Fprint(
				w,
				`[{"requestKey":"one","fileID":"f1","state":"uploading","expectedSize":0,"size":null,"contentType":"application/octet-stream","declaredChecksum":null,"createdAt":"2026-10-15T00:00:00Z","expiresAt":"2026-10-16T00:00:00Z","error":null},{"requestKey":"two","error":{"status":409,"code":"Conflict","message":"different parameters"}}]`,
			)
		case 1:
			if r.Method != "POST" || r.URL.Path != "/v1/storage/files/complete" ||
				string(items[0]) != `"f1"` {
				t.Errorf("confirmation: %s %s %s", r.Method, r.URL.Path, body)
			}
			fmt.Fprint(
				w,
				`[{"fileID":"f1","state":"ready","expectedSize":0,"size":0,"contentType":"application/octet-stream","declaredChecksum":null,"createdAt":"2026-10-15T00:00:00Z","expiresAt":"2026-10-16T00:00:00Z","error":null},{"fileID":"f2","error":{"status":404,"code":"NotFound","message":"not found"}}]`,
			)
		}
	}, 0)
	ctx := api.WithRequestIdentity(
		t.Context(),
		api.RequestIdentity{BearerToken: "actor", TenantID: "tenant", ProjectID: "project"},
	)
	size := int64(0)
	registered, err := client.RegisterFiles(
		ctx,
		api.RegisterFilesRequest{{RequestKey: "one", Size: &size}, {RequestKey: "two", Size: &size}},
	)
	if err != nil || len(registered) != 2 {
		t.Fatalf("register: %+v %v", registered, err)
	}
	if registered[0].FileResponse == nil || registered[0].FileID != "f1" || registered[0].Size != nil ||
		registered[0].DeclaredChecksum != nil ||
		registered[0].Error != nil ||
		registered[1].FileResponse != nil ||
		registered[1].Error.Status != 409 {
		t.Fatalf("results: %+v", registered)
	}
	phase = 1
	completed, err := client.CompleteFiles(ctx, api.CompleteFilesRequest{"f1", "f2"})
	if err != nil || len(completed) != 2 {
		t.Fatalf("complete: %+v %v", completed, err)
	}
	if completed[0].FileID != "f1" || completed[0].FileResponse == nil || completed[0].State != api.FileReady ||
		completed[0].Size == nil ||
		*completed[0].Size != 0 ||
		completed[1].FileResponse != nil ||
		completed[1].Error.Status != 404 {
		t.Fatalf("results: %+v", completed)
	}
}

func TestStorageFileGetAndDelete(t *testing.T) {
	for _, method := range []string{"GET", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			client := storageClientFor(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method || r.URL.EscapedPath() != "/v1/storage/files/f%2Fid" {
					t.Errorf("route: %s %s", r.Method, r.URL.EscapedPath())
				}
				w.Header().Set("Content-Type", "application/json")
				if method == "DELETE" {
					w.WriteHeader(http.StatusAccepted)
				}
				fmt.Fprint(
					w,
					`{"fileID":"f/id","state":"deleting","expectedSize":1,"size":null,"declaredChecksum":null}`,
				)
			}, 0)
			var got *api.FileResponse
			var err error
			if method == "GET" {
				got, err = client.GetFile(t.Context(), "f/id")
			} else {
				got, err = client.DeleteFile(t.Context(), "f/id")
			}
			if err != nil || got.FileID != "f/id" {
				t.Fatalf("result: %+v %v", got, err)
			}
		})
	}
}

func TestStorageFileResultFailureOmitsMetadata(t *testing.T) {
	for _, value := range []any{
		api.RegisterFileResult{RequestKey: "key", Error: &api.FileError{Status: 400, Code: "BadRequest", Message: "invalid"}},
		api.CompleteFileResult{FileID: "f1", Error: &api.FileError{Status: 404, Code: "NotFound", Message: "missing"}},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 2 {
			t.Fatalf("failure includes metadata: %s", data)
		}
	}
}

func TestStorageFileScopesArePerRequest(t *testing.T) {
	client := storageClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Header.Get("X-Tenant-ID") != "tenant-"+token || r.Header.Get("X-Project-ID") != "project-"+token {
			t.Error("scope leaked between concurrent requests")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"fileID":"f1"}`)
	}, 0)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprint(i)
			ctx := api.WithRequestIdentity(
				t.Context(),
				api.RequestIdentity{
					BearerToken: token,
					TenantID:    "tenant-" + token,
					ProjectID:   "project-" + token,
				},
			)
			if _, err := client.GetFile(ctx, "f1"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}
