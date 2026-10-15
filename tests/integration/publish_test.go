package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"gorm.io/gorm"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func publishRaw(t *testing.T, endpoint, artifactID, token, body string, status int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		endpoint+"/v1/artifacts/"+artifactID+"/versions",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("publish status=%d want=%d body=%s", response.StatusCode, status, data)
	}
	return data
}

func published(t *testing.T, data []byte) api.VersionResponse {
	t.Helper()
	var out api.VersionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func versionClient(t *testing.T, endpoint string) *api.Client {
	t.Helper()
	c, err := api.NewClient(api.ClientConfig{Endpoint: endpoint, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestJSONVersions(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		resolver := func(r *http.Request) (service.RequestIdentity, error) {
			if r.Header.Get("Authorization") == "Bearer second-actor" {
				return service.RequestIdentity{
					UserID:         "user-b",
					TenantID:       tenantA,
					OwnershipState: service.OwnershipActive,
				}, nil
			}
			return resolve(r)
		}
		server := fixture.HTTP(t, database, resolver)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		t.Run("immutable-replay-and-actor", func(t *testing.T) {
			a := create(t, client, ctx, "publish-replay", "model")
			body := `{"requestKey":"first","payload":{"hello":"world"}}`
			first := published(t, publishRaw(t, server.URL, a.ID, "tenant", body, 201))
			if first.Version != "1" || first.Sequence != 1 || first.ID == "" || first.Revision != 1 ||
				first.Protected ||
				len(first.Files) != 0 ||
				first.State != "ready" ||
				first.CreatedBy != "user-a" {
				t.Fatalf("first=%+v", first)
			}
			if first.PublishedAt.Location() != time.UTC {
				t.Fatalf("publishedAt=%s", first.PublishedAt)
			}
			again := published(t, publishRaw(t, server.URL, a.ID, "tenant", body, 200))
			if again.ID != first.ID {
				t.Fatal("replay allocated version")
			}
			publishRaw(
				t,
				server.URL,
				a.ID,
				"tenant",
				`{"requestKey":"first","payload":{"hello":"changed"}}`,
				409,
			)
			other := published(t, publishRaw(t, server.URL, a.ID, "second-actor", body, 201))
			if other.ID == first.ID || other.Sequence != 2 || other.CreatedBy != "user-b" {
				t.Fatalf("actor replay=%+v", other)
			}
			read, err := client.GetVersion(
				ctx,
				&api.GetVersionRequest{
					VersionPath: api.VersionPath{
						ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
						VersionID:    first.ID,
					},
				},
			)
			var snapshot map[string]string
			if err == nil {
				err = json.Unmarshal(read.Payload, &snapshot)
			}
			if err != nil || snapshot["hello"] != "world" {
				t.Fatalf("snapshot=%+v %v", read, err)
			}
			_, err = client.ArchiveArtifact(
				ctx,
				&api.ArchiveArtifactRequest{ArtifactPath: api.ArtifactPath{ArtifactID: a.ID}},
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, token := range []string{"tenant", "historical"} {
				replay := published(t, publishRaw(t, server.URL, a.ID, token, body, 200))
				if replay.ID != first.ID {
					t.Fatal("archived/historical replay changed identity")
				}
			}
			publishRaw(t, server.URL, a.ID, "tenant", `{"requestKey":"new","payload":{}}`, 409)
			publishRaw(t, server.URL, a.ID, "historical", `{"requestKey":"new","payload":{}}`, 403)
		})
		t.Run("fingerprint", func(t *testing.T) {
			a := create(t, client, ctx, "fingerprint", "model")
			first := published(
				t,
				publishRaw(
					t,
					server.URL,
					a.ID,
					"tenant",
					`{"requestKey":"equivalent","payload":{"a":1,"b":"x","big":9007199254740993}}`,
					201,
				),
			)
			again := published(
				t,
				publishRaw(
					t,
					server.URL,
					a.ID,
					"tenant",
					`{"requestKey":"equivalent","payload": {"big":9007199254740993,"b":"\u0078","a":1},"annotations":{},"files":[],"aliases":[],"protected":false}`,
					200,
				),
			)
			if again.ID != first.ID {
				t.Fatal("normalization changed fingerprint")
			}
			for _, body := range []string{`{"a":1.0,"b":"x","big":9007199254740993}`, `{"a":1e0,"b":"x","big":9007199254740993}`, `{"a":1,"b":"x","big":9007199254740992}`} {
				publishRaw(
					t,
					server.URL,
					a.ID,
					"tenant",
					`{"requestKey":"equivalent","payload":`+body+`}`,
					409,
				)
			}
			publishRaw(t, server.URL, a.ID, "tenant", `{"requestKey":"array","payload":{"a":[1,2]}}`, 201)
			publishRaw(t, server.URL, a.ID, "tenant", `{"requestKey":"array","payload":{"a":[2,1]}}`, 409)
			dup := published(
				t,
				publishRaw(
					t,
					server.URL,
					a.ID,
					"tenant",
					`{"requestKey":"duplicate","payload":{"a":0,"a":1}}`,
					201,
				),
			)
			normal := published(
				t,
				publishRaw(
					t,
					server.URL,
					a.ID,
					"tenant",
					`{"requestKey":"duplicate","payload":{"a":1}}`,
					200,
				),
			)
			if dup.ID != normal.ID {
				t.Fatal("duplicate key not last-value-wins")
			}
		})
		t.Run("semver-failed-publish-does-not-consume", func(t *testing.T) {
			a, err := client.CreateArtifact(
				ctx,
				&api.CreateArtifactRequest{
					Name:           "semver-publish",
					Type:           "config",
					VersionScheme:  api.VersionSchemeSemver,
					InitialVersion: "1.4.7",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			publishRaw(
				t,
				server.URL,
				a.ID,
				"tenant",
				`{"requestKey":"bad-first","payload":{},"bump":"major"}`,
				400,
			)
			for i, tc := range []struct{ bump, number string }{{bump: "", number: "1.4.7"}, {bump: "patch", number: "1.4.8"}, {bump: "minor", number: "1.5.0"}, {bump: "major", number: "2.0.0"}} {
				body := fmt.Sprintf(`{"requestKey":"s%d","payload":{}`, i)
				if tc.bump != "" {
					body += fmt.Sprintf(`,"bump":%q`, tc.bump)
				}
				body += "}"
				out := published(t, publishRaw(t, server.URL, a.ID, "tenant", body, 201))
				if out.Version != tc.number || out.Sequence != int64(i+1) {
					t.Fatalf("semver=%+v", out)
				}
			}
		})
		t.Run("scope-pagination", func(t *testing.T) {
			a := create(t, client, ctx, "version-pages", "model")
			b := create(t, client, ctx, "version-other", "model")
			var first api.VersionResponse
			for i := 1; i <= 3; i++ {
				v := published(
					t,
					publishRaw(
						t,
						server.URL,
						a.ID,
						"tenant",
						fmt.Sprintf(`{"requestKey":"p%d","payload":{}}`, i),
						201,
					),
				)
				if i == 1 {
					first = v
				}
			}
			for _, token := range []string{"project", "other-tenant", "upper-tenant"} {
				_, err := client.GetVersion(
					identity(t, token),
					&api.GetVersionRequest{
						VersionPath: api.VersionPath{
							ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
							VersionID:    first.ID,
						},
					},
				)
				expectError(t, err, 404, "")
			}
			_, err := client.GetVersion(
				ctx,
				&api.GetVersionRequest{
					VersionPath: api.VersionPath{
						ArtifactPath: api.ArtifactPath{ArtifactID: b.ID},
						VersionID:    first.ID,
					},
				},
			)
			expectError(t, err, 404, "")
			page, err := client.ListVersions(
				ctx,
				&api.ListVersionsRequest{
					ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
					PageQuery:    api.PageQuery{Limit: 1},
				},
			)
			if err != nil || len(page.Items) != 1 || page.Items[0].Sequence != 3 || page.NextCursor == "" {
				t.Fatalf("page=%+v %v", page, err)
			}
			cursor := page.NextCursor
			for sequence := int64(2); sequence >= 1; sequence-- {
				page, err = client.ListVersions(
					ctx,
					&api.ListVersionsRequest{
						ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
						PageQuery:    api.PageQuery{Limit: 1, Cursor: cursor},
					},
				)
				if err != nil || len(page.Items) != 1 || page.Items[0].Sequence != sequence {
					t.Fatalf("page=%+v %v", page, err)
				}
				cursor = page.NextCursor
			}
			if cursor != "" {
				t.Fatal("last page has cursor")
			}
			filtered, err := client.ListVersions(
				ctx,
				&api.ListVersionsRequest{ArtifactPath: api.ArtifactPath{ArtifactID: a.ID}, Number: "1"},
			)
			if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != first.ID {
				t.Fatalf("number=%+v %v", filtered, err)
			}
		})
		t.Run("reject-invalid-and-future-input", func(t *testing.T) {
			a := create(t, client, ctx, "invalid-input", "model")
			for i, extra := range []string{`"payload":null`, `"payload":[]`, `"annotations":null,"payload":{}`, `"files":null,"payload":{}`, `"Files":null,"payload":{}`, `"DraftID":null,"payload":{}`, `"ExpectedDraftRevision":null,"payload":{}`, `"Aliases":null,"payload":{}`, `"bump":"patch","payload":{}`, `"payload":{},"extra":1`} {
				publishRaw(
					t,
					server.URL,
					a.ID,
					"tenant",
					fmt.Sprintf(`{"requestKey":"invalid-%d",%s}`, i, extra),
					400,
				)
			}
			v := published(
				t,
				publishRaw(t, server.URL, a.ID, "tenant", `{"requestKey":"valid","payload":{}}`, 201),
			)
			if v.Sequence != 1 {
				t.Fatal("invalid request consumed number")
			}
		})
		t.Run("request-key-is-exact", func(t *testing.T) {
			a := create(t, client, ctx, "exact-key", "model")
			for i, key := range []string{"key", "key ", "KEY"} {
				v := published(
					t,
					publishRaw(
						t,
						server.URL,
						a.ID,
						"tenant",
						fmt.Sprintf(`{"requestKey":%q,"payload":{}}`, key),
						201,
					),
				)
				if v.Sequence != int64(i+1) {
					t.Fatalf("request key %q collapsed: %+v", key, v)
				}
			}
		})
		t.Run("field-byte-boundaries", func(t *testing.T) {
			a := create(t, client, ctx, "byte-limits", "model")
			for _, tc := range []struct {
				field string
				limit int
				code  string
			}{{field: "payload", limit: 262144, code: "PayloadTooLarge"}, {field: "annotations", limit: 65536, code: "AnnotationsTooLarge"}} {
				for _, delta := range []int{0, 1} {
					value := `{"x":"` + strings.Repeat("a", tc.limit-8+delta) + `"}`
					body := fmt.Sprintf(`{"requestKey":"%s-%d",`, tc.field, delta)
					if tc.field != "payload" {
						body += `"payload":{},`
					}
					body += fmt.Sprintf(`%q:%s}`, tc.field, value)
					status := 201
					if delta == 1 {
						status = 413
					}
					data := publishRaw(t, server.URL, a.ID, "tenant", body, status)
					if delta == 1 && !strings.Contains(string(data), tc.code) {
						t.Fatalf("error=%s", data)
					}
				}
			}
		})
	})
}

func TestPublishReplayAfterLimitChange(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		initial := fixture.HTTP(t, database, resolve)
		client := versionClient(t, initial.URL)
		a := create(t, client, identity(t, "tenant"), "changed-limits", "model")
		body := `{"requestKey":"before","payload":{"data":"larger than new limit"}}`
		first := published(t, publishRaw(t, initial.URL, a.ID, "tenant", body, 201))
		lexical := fixture.HTTPWithOptions(
			t,
			database,
			resolve,
			&service.VersionConfig{
				MaxFilesPerVersion:  1000,
				MaxPayloadBytes:     14,
				MaxAnnotationsBytes: 14,
				MaxRequestBodyBytes: 1024,
			},
		)
		// Compact removes whitespace but preserves duplicate keys and escape spelling.
		publishRaw(t, lexical.URL, a.ID, "tenant", `{"requestKey":"escaped-fit","payload":{"x":"\u0061"}}`, 201)
		publishRaw(
			t,
			lexical.URL,
			a.ID,
			"tenant",
			`{"requestKey":"escaped-over","payload":{"x":"\u0061\u0061"}}`,
			413,
		)
		publishRaw(
			t,
			lexical.URL,
			a.ID,
			"tenant",
			`{"requestKey":"duplicate-over","payload":{"x":0,"x":0,"x":0}}`,
			413,
		)
		publishRaw(t, lexical.URL, a.ID, "tenant", `{"requestKey":"unicode-fit","payload":{"x":"中文"}}`, 201)
		publishRaw(t, lexical.URL, a.ID, "tenant", `{"requestKey":"unicode-over","payload":{"x":"中文字"}}`, 413)
		restricted := fixture.HTTPWithOptions(
			t,
			database,
			resolve,
			&service.VersionConfig{
				MaxFilesPerVersion:  1000,
				MaxPayloadBytes:     8,
				MaxAnnotationsBytes: 8,
				MaxRequestBodyBytes: 1024,
			},
		)
		again := published(t, publishRaw(t, restricted.URL, a.ID, "tenant", body, 200))
		if again.ID != first.ID {
			t.Fatal("lowered field limit broke replay")
		}
		publishRaw(t, restricted.URL, a.ID, "tenant", strings.Replace(body, "before", "new-key", 1), 413)
		publishRaw(t, restricted.URL, a.ID, "tenant", body+strings.Repeat(" ", 1025), 413)
	})
}

// The adapter delegates all SQL to the real database, changing only the COMMIT acknowledgement.
type uncertainCommitPool struct {
	gorm.ConnPool
	connection *sql.DB
	persist    bool
	commits    atomic.Int32
}

func (p *uncertainCommitPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.connection.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	if options != nil && options.ReadOnly {
		return tx, nil
	}
	return &uncertainCommitTx{Tx: tx, pool: p}, nil
}

type uncertainCommitTx struct {
	*sql.Tx
	pool *uncertainCommitPool
}

func (tx *uncertainCommitTx) Commit() error {
	tx.pool.commits.Add(1)
	var err error
	if tx.pool.persist {
		err = tx.Tx.Commit()
	} else {
		err = tx.Tx.Rollback()
	}
	if err != nil {
		return err
	}
	return errors.New("test connection lost during COMMIT acknowledgement")
}

func TestPublishUnknownCommitOutcome(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		a := create(t, client, identity(t, "tenant"), "unknown-commit", "model")
		conn, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		original := database.Statement.ConnPool
		for _, persist := range []bool{true, false} {
			pool := &uncertainCommitPool{ConnPool: conn, connection: conn, persist: persist}
			database.Statement.ConnPool = pool
			body := fmt.Sprintf(`{"requestKey":"uncertain-%t","payload":{}}`, persist)
			status := 503
			if persist {
				status = 200
			}
			data := publishRaw(t, server.URL, a.ID, "tenant", body, status)
			database.Statement.ConnPool = original
			if pool.commits.Load() != 1 {
				t.Fatalf("unknown commit retried %d times", pool.commits.Load())
			}
			if persist {
				first := published(t, data)
				again := published(t, publishRaw(t, server.URL, a.ID, "tenant", body, 200))
				if first.ID != again.ID || first.Sequence != 1 {
					t.Fatal("commit acknowledgement loss created duplicate")
				}
			} else {
				if !strings.Contains(string(data), "OperationOutcomeUnknown") {
					t.Fatalf("unknown outcome error=%s", data)
				}
				next := published(t, publishRaw(t, server.URL, a.ID, "tenant", body, 201))
				if next.Sequence != 2 {
					t.Fatalf("failed commit consumed counter: %+v", next)
				}
			}
		}
	})
}

func TestJSONNumericLexemesSurvivePersistence(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "numeric-lexemes", "model")
		for i, value := range []string{"184467440737095516160001", "0.123456789012345678901234567890123456789", "1.000000000000000000000000000000000000001", "1e400"} {
			body := fmt.Sprintf(`{"requestKey":"numeric-%d","payload":{"n":%s}}`, i, value)
			v := published(t, publishRaw(t, server.URL, a.ID, "tenant", body, 201))
			read, err := client.GetVersion(
				ctx,
				&api.GetVersionRequest{
					VersionPath: api.VersionPath{
						ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
						VersionID:    v.ID,
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(read.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if string(payload["n"]) != value {
				t.Fatalf("numeric lexeme changed: got %s want %s", payload["n"], value)
			}
		}
	})
}
