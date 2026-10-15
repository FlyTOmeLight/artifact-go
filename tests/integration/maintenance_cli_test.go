package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
)

func TestMaintenanceCLIRecovery(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "artifact-maintenance")
	build := exec.CommandContext(t.Context(), "go", "build", "-race", "-o", binary, "./cmd/maintenance")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("maintenance build: %v %s", err, output)
	}
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			for _, domain := range []string{"artifact", "storage"} {
				t.Run(domain, func(t *testing.T) {
					cfg, database := isolatedReferenceDB(t, driver)
					name := "background_tasks"
					if domain == "storage" {
						name = "storage_tasks"
						if err := st.Migrate(t.Context(), database); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := table.Migrate(t.Context(), database); err != nil {
							t.Fatal(err)
						}
					}
					configPath := filepath.Join(t.TempDir(), "database.json")
					raw, err := json.Marshal(cfg)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(configPath, raw, 0o600); err != nil {
						t.Fatal(err)
					}
					notBefore := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
					payload := []byte(
						`{"scope":{"tenantID":"test"},"references":[{"referenceID":"retained-ref","fileID":"retained-file"}]}`,
					)
					row := map[string]any{
						"id":              "failed-task",
						"kind":            "reference-release",
						"target_id":       "retained-target",
						"payload":         payload,
						"state":           "failed",
						"next_run_at":     notBefore,
						"not_before":      notBefore,
						"lease_token":     "expired-lease",
						"attempts":        20,
						"total_attempts":  27,
						"run_generation":  2,
						"revision":        9,
						"recovery_events": []byte(`[]`),
						"last_error":      "controlled failure",
					}
					if domain == "storage" {
						row["kind"] = "delete"
						payload = []byte(`{}`)
						row["payload"] = payload
					}
					row["created_at"] = time.Now().UTC().Truncate(time.Second)
					row["updated_at"] = row["created_at"]
					if err = database.Table(name).Create(row).Error; err != nil {
						t.Fatal(err)
					}
					read := func() table.BackgroundTask {
						var value table.BackgroundTask
						if e := database.Table(name).
							Where("id = ?", "failed-task").
							First(&value).
							Error; e != nil {
							t.Fatal(e)
						}
						return value
					}
					before := read()
					args := []string{
						"--domain",
						domain,
						"--db-config",
						configPath,
						"--task-id",
						"failed-task",
						"--expected-revision",
						"9",
						"--operator",
						"fixture-operator",
						"--reason",
						"verified dependency recovered",
						"--record",
						"T12-fixture",
					}
					run := func(execute bool) ([]byte, error) {
						call := append([]string(nil), args...)
						if execute {
							call = append(call, "--execute")
						}
						return exec.CommandContext(t.Context(), binary, call...).
							CombinedOutput()
					}
					output, err := run(false)
					if err != nil {
						t.Fatalf("dry run: %v %s", err, output)
					}
					var preview struct {
						Applied bool
						Before  backgroundtask.RecoveryPreview
					}
					if err = json.Unmarshal(bytes.TrimSpace(output), &preview); err != nil {
						t.Fatalf("dry-run JSON: %s", output)
					}
					digest := sha256.Sum256(payload)
					if preview.Applied ||
						preview.Before.PayloadSHA256 != hex.EncodeToString(digest[:]) ||
						preview.Before.Revision != 9 {
						t.Fatalf("incorrect preview %+v", preview)
					}
					if !reflect.DeepEqual(before, read()) {
						t.Fatal("dry-run changed persisted task")
					}
					output, err = run(true)
					if err != nil {
						t.Fatalf("execute: %v %s", err, output)
					}
					var applied struct{ Applied bool }
					if json.Unmarshal(bytes.TrimSpace(output), &applied) != nil ||
						!applied.Applied {
						t.Fatalf("execute result: %s", output)
					}
					after := read()
					if !bytes.Equal(after.Payload, before.Payload) ||
						after.TargetID != before.TargetID ||
						!after.NotBefore.Equal(notBefore) ||
						!after.NextRunAt.Equal(notBefore) {
						t.Fatal(
							"requeue changed the immutable obligation or shortened notBefore",
						)
					}
					if after.State != "pending" || after.Revision != 10 ||
						after.RunGeneration != 3 ||
						after.Attempts != 0 ||
						after.TotalAttempts != 27 ||
						after.LeaseToken != "" ||
						after.LeaseUntil != nil {
						t.Fatalf("invalid recovered task %+v", after)
					}
					var events []backgroundtask.RecoveryEvent
					if err = json.Unmarshal(
						after.RecoveryEvents,
						&events,
					); err != nil ||
						len(events) != 1 {
						t.Fatalf("missing audit: %s %v", after.RecoveryEvents, err)
					}
					event := events[0]
					if event.Operator != "fixture-operator" || event.Record != "T12-fixture" ||
						event.Reason != "verified dependency recovered" ||
						event.PreviousRevision != 9 ||
						event.PreviousGeneration != 2 ||
						event.PreviousFailures != 20 ||
						event.PreviousError != "controlled failure" ||
						event.From != "failed" ||
						event.To != "pending" ||
						event.At.IsZero() {
						t.Fatalf("incomplete recovery audit %+v", event)
					}
					if _, err = run(true); err == nil {
						t.Fatal("old revision replay unexpectedly succeeded")
					}
					if !reflect.DeepEqual(after, read()) {
						t.Fatal("rejected replay changed the task")
					}
				})
			}
		})
	}
}
