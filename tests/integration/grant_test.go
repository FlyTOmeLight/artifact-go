package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

type grantSTS struct {
	calls  atomic.Int32
	fail   atomic.Bool
	onSign func()
	policy string
}

func (s *grantSTS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls.Add(1)
	_ = r.ParseForm()
	s.policy = r.Form.Get("Policy")
	if s.onSign != nil {
		s.onSign()
	}
	w.Header().Set("Content-Type", "text/xml")
	if s.fail.Load() {
		w.WriteHeader(503)
		fmt.Fprint(
			w,
			`<ErrorResponse><Error><Code>ServiceUnavailable</Code><Message>fixture</Message></Error></ErrorResponse>`,
		)
		return
	}
	fmt.Fprintf(
		w,
		`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>temporary</AccessKeyId><SecretAccessKey>fixture-secret</SecretAccessKey><SessionToken>fixture-token</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`,
		time.Now().UTC().Add(950*time.Second).Format(time.RFC3339),
	)
}

func grantHTTP(t *testing.T, database *db.DB) (*api.Client, *httptest.Server, *grantSTS) {
	return grantHTTPConfigured(t, database, nil)
}

func grantHTTPConfigured(
	t *testing.T,
	database *db.DB,
	grants *service.StorageGrantConfig,
) (*api.Client, *httptest.Server, *grantSTS) {
	t.Helper()
	s3 := httptest.NewServer(&storageFileS3{})
	t.Cleanup(s3.Close)
	sts := &grantSTS{}
	issuer := httptest.NewServer(sts)
	t.Cleanup(issuer.Close)
	c, s, _ := storageFileServer(
		t,
		database,
		service.StorageOptions{
			Config: &service.StorageConfig{
				BackendID:       "fixture",
				Endpoint:        s3.URL,
				Region:          "us-east-1",
				AccessKeyID:     "fixture-admin",
				SecretAccessKey: "fixture-secret",
				UsePathStyle:    true,
				GrantIssuer: &service.StorageGrantIssuerConfig{
					Endpoint:        issuer.URL,
					AccessKeyID:     "fixture-issuer",
					SecretAccessKey: "fixture-secret",
				},
			},
			IdentityResolver:      resolve,
			TenantServiceVerifier: func(*http.Request) error { return nil },
			AutoMigrate:           true,
			Grants:                grants,
		},
	)
	return c, s, sts
}

func grantFileRow(t *testing.T, database *db.DB, id string) st.StorageFile {
	t.Helper()
	var f st.StorageFile
	if err := database.Where("id = ?", id).First(&f).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func TestStorageGrantLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		c, _, sts := grantHTTP(t, database)
		ctx := storageIdentity(t, "tenant")
		first := storageRegister(t, c, ctx, "first", 0)
		second := storageRegister(t, c, ctx, "second", 0)
		request := &api.CreateGrantRequest{
			FileIDs: []string{second.FileID, first.FileID},
			Scope:   api.GrantScope{Preset: "upload"},
		}
		got, err := c.CreateGrant(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Files) != 2 || got.Files[0].FileID != second.FileID ||
			strings.Join(
				got.Actions,
				",",
			) != "s3:AbortMultipartUpload,s3:ListMultipartUploadParts,s3:PutObject" ||
			got.Credentials.SessionToken == "" {
			t.Fatalf("response %+v", got)
		}
		for _, id := range request.FileIDs {
			f := grantFileRow(t, database, id)
			if f.LastGrantExpiresAt == nil || f.LastGrantExpiresAt.Before(got.ExpiresAt) {
				t.Fatal("missing grant reservation")
			}
		}
		var policy struct {
			Statement []struct {
				Action   []string
				Resource []string
			}
		}
		if err := json.Unmarshal(
			[]byte(sts.policy),
			&policy,
		); err != nil || len(policy.Statement) != 1 ||
			len(policy.Statement[0].Resource) != 2 {
			t.Fatalf("policy %s %v", sts.policy, err)
		}
		for _, resource := range policy.Statement[0].Resource {
			if strings.Contains(resource, "*") {
				t.Fatal("wildcard resource")
			}
		}
		_, err = c.CreateGrant(storageIdentity(t, "other-tenant"), request)
		expectError(t, err, 404, "")
		_, err = c.CreateGrant(storageIdentity(t, "historical"), request)
		expectError(t, err, 403, "ScopeInactive")
		request.Scope = api.GrantScope{Actions: []string{"s3:GetObject", "s3:PutObject"}}
		_, err = c.CreateGrant(ctx, request)
		expectError(t, err, 409, "")
		completed, err := c.CompleteFiles(ctx, api.CompleteFilesRequest{first.FileID, second.FileID})
		if err != nil || completed[0].Error != nil || completed[1].Error != nil {
			t.Fatalf("complete %+v %v", completed, err)
		}
		before := grantFileRow(t, database, first.FileID)
		request.Scope = api.GrantScope{Preset: "download"}
		got, err = c.CreateGrant(storageIdentity(t, "historical"), request)
		if err != nil || len(got.Actions) != 1 || got.Actions[0] != "s3:GetObject" {
			t.Fatalf("read %+v %v", got, err)
		}
		after := grantFileRow(t, database, first.FileID)
		if !before.LastGrantExpiresAt.Equal(*after.LastGrantExpiresAt) {
			t.Fatal("read changed upload reservation")
		}
		request.Scope = api.GrantScope{Preset: "upload"}
		_, err = c.CreateGrant(ctx, request)
		expectError(t, err, 409, "")
		request.Scope = api.GrantScope{Preset: "download"}
		if err := database.Model(&st.StorageFile{}).
			Where("id = ?", first.FileID).
			Update("expires_at", time.Now().Add(-time.Minute)).
			Error; err != nil {
			t.Fatal(err)
		}
		calls := sts.calls.Load()
		_, err = c.CreateGrant(ctx, request)
		expectError(t, err, 409, "")
		if sts.calls.Load() != calls {
			t.Fatal("partially signed invalid batch")
		}
	})
}

func TestStorageGrantRequestValidation(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		c, server, sts := grantHTTP(t, database)
		file := storageRegister(t, c, storageIdentity(t, "tenant"), "validation", 0)
		scopes := []string{
			`null`,
			`{}`,
			`{"preset":"upload","actions":null}`,
			`{"preset":"upload","actions":[]}`,
			`{"actions":[]}`,
			`{"actions":["s3:GetObject","s3:GetObject"]}`,
			`{"actions":["s3:*"]}`,
			`{"actions":["s3:DeleteObject"]}`,
			`{"preset":"other"}`,
			`{"preset":null}`,
			`{"preset":"upload","unknown":true}`,
		}
		bodies := []string{
			`null`,
			`[]`,
			`{}`,
			fmt.Sprintf(`{"fileIDs":[%q,%q],"scope":{"preset":"upload"}}`, file.FileID, file.FileID),
		}
		for _, scope := range scopes {
			bodies = append(bodies, fmt.Sprintf(`{"fileIDs":[%q],"scope":%s}`, file.FileID, scope))
		}
		for _, ttl := range []string{"null", "0", "-1", "1.5", "900"} {
			bodies = append(
				bodies,
				fmt.Sprintf(
					`{"fileIDs":[%q],"scope":{"preset":"upload"},"ttlSeconds":%s}`,
					file.FileID,
					ttl,
				),
			)
		}
		for _, body := range bodies {
			req, _ := http.NewRequestWithContext(
				t.Context(),
				"POST",
				server.URL+"/v1/storage/grants",
				strings.NewReader(body),
			)
			req.Header.Set("Authorization", "Bearer tenant")
			req.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != 400 {
				t.Fatalf("body=%s status=%d response=%s", body, response.StatusCode, data)
			}
		}
		if sts.calls.Load() != 0 {
			t.Fatal("invalid request reached STS")
		}
	})
}

func TestStorageGrantSigningFailureAndRace(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		c, _, sts := grantHTTP(t, database)
		ctx := storageIdentity(t, "tenant")
		file := storageRegister(t, c, ctx, "failed", 0)
		req := &api.CreateGrantRequest{FileIDs: []string{file.FileID}, Scope: api.GrantScope{Preset: "upload"}}
		sts.fail.Store(true)
		result, err := c.CreateGrant(ctx, req)
		if err == nil || result != nil {
			t.Fatal("sign failure returned credentials")
		}
		reserved := grantFileRow(t, database, file.FileID)
		if reserved.LastGrantExpiresAt == nil {
			t.Fatal("failed STS lost reservation")
		}
		sts.fail.Store(false)
		sts.onSign = func() {
			completed, err := c.CompleteFiles(ctx, api.CompleteFilesRequest{file.FileID})
			if err != nil || completed[0].Error != nil {
				t.Errorf("complete while signing %+v %v", completed, err)
			}
		}
		result, err = c.CreateGrant(ctx, req)
		expectError(t, err, 409, "")
		if result != nil {
			t.Fatal("returned upload credential after complete")
		}
		after := grantFileRow(t, database, file.FileID)
		if after.LastGrantExpiresAt.Before(*reserved.LastGrantExpiresAt) {
			t.Fatal("reservation shortened")
		}
	})
}

func TestStorageGrantAllowedActionsAndTTLCap(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		c, _, sts := grantHTTPConfigured(
			t,
			database,
			&service.StorageGrantConfig{
				UploadTTL:      time.Hour,
				ReadTTL:        time.Hour,
				AllowedActions: []string{"s3:PutObject"},
			},
		)
		ctx := storageIdentity(t, "tenant")
		file := storageRegister(t, c, ctx, "allowed", 0)
		_, err := c.CreateGrant(
			ctx,
			&api.CreateGrantRequest{
				FileIDs: []string{file.FileID},
				Scope:   api.GrantScope{Preset: "upload"},
			},
		)
		expectError(t, err, 403, "GrantScopeDenied")
		if sts.calls.Load() != 0 {
			t.Fatal("denied preset reached STS")
		}
		got, err := c.CreateGrant(
			ctx,
			&api.CreateGrantRequest{
				FileIDs: []string{file.FileID},
				Scope:   api.GrantScope{Actions: []string{"s3:PutObject"}},
			},
		)
		if err != nil || len(got.Actions) != 1 {
			t.Fatalf("allowed subset %v", err)
		}
		reservation := grantFileRow(t, database, file.FileID).LastGrantExpiresAt
		if reservation == nil || reservation.After(time.Now().Add(time.Hour)) {
			t.Fatal("reservation exceeds configured TTL")
		}
	})
}
