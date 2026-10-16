package unit_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	client "github.com/FlyTOmeLight/artifact-go/internal/client/storage"
)

func TestStorageObjectExactKeyMultipartCleanup(t *testing.T) {
	var actions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			actions = append(actions, "list")
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(
				w,
				`<ListMultipartUploadsResult><IsTruncated>false</IsTruncated><Upload><Key>files/f1</Key><UploadId>target</UploadId></Upload><Upload><Key>files/f10</Key><UploadId>neighbor</UploadId></Upload></ListMultipartUploadsResult>`,
			)
			return
		}
		if r.Method == "DELETE" {
			actions = append(actions, "delete:"+r.URL.Path+":"+r.URL.Query().Get("uploadId"))
			w.WriteHeader(204)
			return
		}
		t.Errorf("unexpected %s", r.Method)
	}))
	defer server.Close()
	backend, err := client.New(
		client.Config{
			BackendID:       "test",
			Endpoint:        server.URL,
			Region:          "us-east-1",
			AccessKeyID:     "key",
			SecretAccessKey: "secret",
			UsePathStyle:    true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	found, err := backend.HasIncompleteUploads(t.Context(), "bucket", "files/f1")
	if err != nil || !found {
		t.Fatalf("found=%v error=%v", found, err)
	}
	actions = nil
	if err = backend.Delete(t.Context(), "bucket", "files/f1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"list", "delete:/bucket/files/f1:target", "delete:/bucket/files/f1:"}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions %v, want %v", actions, want)
	}
}

func TestStorageObjectStat(t *testing.T) {
	for _, status := range []int{200, 404, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == 200 {
					w.Header().Set("Content-Length", "17")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			backend, err := client.New(
				client.Config{
					BackendID:       "test",
					Endpoint:        server.URL,
					Region:          "us-east-1",
					AccessKeyID:     "key",
					SecretAccessKey: "secret",
					UsePathStyle:    true,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			info, err := backend.Stat(t.Context(), "bucket", "key")
			if status == 200 {
				if err != nil || info.Size != 17 {
					t.Fatalf("info=%v err=%v", info, err)
				}
			} else if status == 404 {
				if !errors.Is(err, client.ErrObjectNotFound) {
					t.Fatalf("error=%v", err)
				}
			} else if err == nil || errors.Is(err, client.ErrObjectNotFound) {
				t.Fatalf("access denial must not be absence: %v", err)
			}
		})
	}
}

func TestStorageMultipartRepeatedPageFailsClosed(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(
			w,
			`<ListMultipartUploadsResult><IsTruncated>true</IsTruncated><NextKeyMarker>same</NextKeyMarker><NextUploadIdMarker>same</NextUploadIdMarker></ListMultipartUploadsResult>`,
		)
	}))
	defer server.Close()
	backend, err := client.New(
		client.Config{
			BackendID:       "test",
			Endpoint:        server.URL,
			Region:          "us-east-1",
			AccessKeyID:     "key",
			SecretAccessKey: "secret",
			UsePathStyle:    true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.Delete(t.Context(), "bucket", "files/f1"); err == nil {
		t.Fatal("repeated pagination must fail")
	}
	if calls != 2 {
		t.Fatalf("expected bounded pagination, calls=%d", calls)
	}
	calls = 0
	if _, err = backend.HasIncompleteUploads(t.Context(), "bucket", "files/f1"); err == nil {
		t.Fatal("repeated pagination must fail")
	}
	if calls != 2 {
		t.Fatalf("expected bounded pagination, calls=%d", calls)
	}
}

func TestStorageObjectMissingBucketCleanup(t *testing.T) {
	for _, code := range []string{"NoSuchBucket", "AccessDenied"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				status := 404
				if code == "AccessDenied" {
					status = 403
				}
				w.WriteHeader(status)
				fmt.Fprintf(w, "<Error><Code>%s</Code></Error>", code)
			}))
			defer server.Close()
			backend, err := client.New(
				client.Config{
					BackendID:       "test",
					Endpoint:        server.URL,
					Region:          "us-east-1",
					AccessKeyID:     "key",
					SecretAccessKey: "secret",
					UsePathStyle:    true,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			err = backend.Delete(t.Context(), "bucket", "files/f1")
			if (err == nil) != (code == "NoSuchBucket") {
				t.Fatalf("delete %s: %v", code, err)
			}
			found, err := backend.HasIncompleteUploads(t.Context(), "bucket", "files/f1")
			if found || (err == nil) != (code == "NoSuchBucket") {
				t.Fatalf("multipart %s: %v %v", code, found, err)
			}
		})
	}
}
