package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func patchVersion(t *testing.T, url, body string, status int) {
	t.Helper()
	r, e := http.NewRequestWithContext(t.Context(), http.MethodPatch, url, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer tenant")
	r.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Fatalf("PATCH status=%d want=%d body=%s", resp.StatusCode, status, b)
	}
}

func TestVersionManagement(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "management", "model")
		v := publishVersion(t, c, a.ID, "one")
		p := api.VersionPath{ArtifactPath: api.ArtifactPath{ArtifactID: a.ID}, VersionID: v.ID}
		_, zeroErr := c.UpdateAnnotations(
			ctx,
			&api.UpdateAnnotationsRequest{
				VersionPath:      p,
				ExpectedRevision: revision(0),
				Annotations:      json.RawMessage(`{}`),
			},
		)
		expectError(t, zeroErr, 409, api.CodeRevisionConflict)
		_, zeroErr = c.UpdateProtection(
			ctx,
			&api.UpdateProtectionRequest{
				VersionPath:      p,
				ExpectedRevision: revision(0),
				Protected:        flag(false),
			},
		)
		expectError(t, zeroErr, 409, api.CodeRevisionConflict)
		updated, err := c.UpdateAnnotations(
			ctx,
			&api.UpdateAnnotationsRequest{
				VersionPath:      p,
				ExpectedRevision: revision(1),
				Annotations:      json.RawMessage(`{"note":"new","n":9007199254740993}`),
			},
		)
		if err != nil || updated.Revision != 2 ||
			!strings.Contains(string(updated.Annotations), "9007199254740993") {
			t.Fatalf("annotations %+v %v", updated, err)
		}
		_, err = c.UpdateProtection(
			ctx,
			&api.UpdateProtectionRequest{
				VersionPath:      p,
				ExpectedRevision: revision(1),
				Protected:        flag(true),
			},
		)
		expectError(t, err, 409, api.CodeRevisionConflict)
		var wg sync.WaitGroup
		result := make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, e := c.UpdateAnnotations(
				ctx,
				&api.UpdateAnnotationsRequest{
					VersionPath:      p,
					ExpectedRevision: revision(2),
					Annotations:      json.RawMessage(`{}`),
				},
			)
			result <- e
		}()
		go func() {
			defer wg.Done()
			_, e := c.UpdateProtection(
				ctx,
				&api.UpdateProtectionRequest{
					VersionPath:      p,
					ExpectedRevision: revision(2),
					Protected:        flag(true),
				},
			)
			result <- e
		}()
		wg.Wait()
		close(result)
		wins := 0
		for e := range result {
			if e == nil {
				wins++
			} else {
				expectError(t, e, 409, api.CodeRevisionConflict)
			}
		}
		if wins != 1 {
			t.Fatalf("shared revision winners %d", wins)
		}
		_, err = c.UpdateProtection(
			identity(t, "project"),
			&api.UpdateProtectionRequest{
				VersionPath:      p,
				ExpectedRevision: revision(3),
				Protected:        flag(false),
			},
		)
		expectError(t, err, 404, "")
		_, err = c.ArchiveArtifact(ctx, &api.ArchiveArtifactRequest{ArtifactPath: p.ArtifactPath})
		if err != nil {
			t.Fatal(err)
		}
		updated, err = c.UpdateProtection(
			identity(t, "historical"),
			&api.UpdateProtectionRequest{
				VersionPath:      p,
				ExpectedRevision: revision(3),
				Protected:        flag(false),
			},
		)
		if err != nil || updated.Protected || updated.Revision != 4 {
			t.Fatalf("historical manage %+v %v", updated, err)
		}
		root := server.URL + "/v1/artifacts/" + a.ID + "/versions/" + v.ID
		for _, body := range []string{`{}`, `{"expectedRevision":null,"annotations":{}}`, `{"expectedRevision":4,"annotations":null}`, `{"expectedRevision":4,"annotations":[]}`, `{"expectedRevision":4}`, `{"expectedRevision":4,"annotations":{},"payload":{}}`} {
			patchVersion(t, root+"/annotations", body, 400)
		}
		for _, body := range []string{`{"expectedRevision":4}`, `{"expectedRevision":4,"protected":null}`, `{"expectedRevision":null,"protected":false}`} {
			patchVersion(t, root+"/protection", body, 400)
		}
		// Compact UTF-8 JSON bytes, including quotes and field syntax, count toward 64 KiB.
		exact := `{"x":"` + strings.Repeat("a", (64<<10)-8) + `"}`
		patchVersion(t, root+"/annotations", `{"expectedRevision":4,"annotations":`+exact+`}`, 200)
		over := `{"x":"` + strings.Repeat("a", (64<<10)-7) + `"}`
		patchVersion(t, root+"/annotations", `{"expectedRevision":5,"annotations":`+over+`}`, 413)
		final, err := c.GetVersion(ctx, &api.GetVersionRequest{VersionPath: p})
		if err != nil || final.Revision != 5 || string(final.Payload) != "{}" ||
			len(final.Annotations) != (64<<10) {
			t.Fatalf("final %+v %v", final, err)
		}
	})
}
