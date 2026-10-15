package integration_test

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// Direct DB mutations represent retained deletion identities; T02 does not expose deletion APIs.
func TestVersionIdentityConstraints(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		client := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, client, ctx, "identity-a", "model")
		b := create(t, client, ctx, "identity-b", "model")
		body := `{"requestKey":"original","payload":{"secret":"must-not-reappear"}}`
		v := published(t, publishRaw(t, server.URL, a.ID, "tenant", body, 201))
		wrong := table.CreateRequest{
			ArtifactID:  b.ID,
			ActorID:     "user-a",
			RequestKey:  "cross-owner",
			RequestHash: "hash",
			VersionID:   v.ID,
			CreatedAt:   time.Now().UTC(),
		}
		if err := database.Create(&wrong).Error; err == nil {
			t.Fatal("cross-collection receipt foreign key accepted")
		}
		if err := database.Model(&table.Version{}).
			Where("id = ?", v.ID).
			Updates(map[string]any{"state": "deleted", "payload": json.RawMessage(`{}`), "annotations": json.RawMessage(`{}`), "deleted_at": time.Now().UTC()}).
			Error; err != nil {
			t.Fatal(err)
		}
		data := publishRaw(t, server.URL, a.ID, "tenant", body, 410)
		var deleted struct {
			Version api.VersionIdentity `json:"version"`
			Payload json.RawMessage     `json:"payload"`
		}
		if err := json.Unmarshal(data, &deleted); err != nil {
			t.Fatal(err)
		}
		if deleted.Version.ID != v.ID || deleted.Version.Sequence != 1 || len(deleted.Payload) != 0 {
			t.Fatalf("deleted receipt=%s", data)
		}
		_, err := client.GetVersion(
			ctx,
			&api.GetVersionRequest{
				VersionPath: api.VersionPath{
					ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
					VersionID:    v.ID,
				},
			},
		)
		expectError(t, err, 404, "")
		next := published(
			t,
			publishRaw(t, server.URL, a.ID, "tenant", `{"requestKey":"next","payload":{}}`, 201),
		)
		if next.Sequence != 2 || next.Version != "2" {
			t.Fatalf("tombstone reused number=%+v", next)
		}
		duplicate := table.Version{
			ID:          "duplicate",
			ArtifactID:  a.ID,
			Sequence:    1,
			Number:      "1",
			Payload:     json.RawMessage(`{}`),
			Annotations: json.RawMessage(`{}`),
			Revision:    1,
			State:       "ready",
			CreatedBy:   "user-a",
			PublishedAt: time.Now().UTC(),
		}
		duplicate.Number = "99"
		if err := database.Create(&duplicate).Error; err == nil {
			t.Fatal("tombstone sequence uniqueness lost")
		}
		duplicate.Sequence = 99
		duplicate.Number = "1"
		if err := database.Create(&duplicate).Error; err == nil {
			t.Fatal("tombstone number uniqueness lost")
		}
		if err := database.Model(&table.Artifact{}).
			Where("id = ?", a.ID).
			Update("last_sequence", int64(math.MaxInt64)).
			Error; err != nil {
			t.Fatal(err)
		}
		publishRaw(t, server.URL, a.ID, "tenant", `{"requestKey":"overflow","payload":{}}`, 409)
		var count int64
		if err := database.Model(&table.CreateRequest{}).
			Where("artifact_id = ? AND request_key = ?", a.ID, "overflow").
			Count(&count).
			Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("failed overflow persisted successful receipt")
		}
	})
}
