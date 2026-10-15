package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestMaintenanceCommand(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "artifact-maintenance")
	buildCtx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "./cmd/maintenance")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build maintenance: %v\n%s", err, output)
	}
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		fixture.HTTP(t, database, resolve)
		var cfg db.Config
		var name string
		if database.Dialector.Name() == "postgres" {
			cfg = fixture.Config(t, db.DriverPostgreSQL)
			if err := database.Raw("SELECT current_schema()").Scan(&name).Error; err != nil {
				t.Fatal(err)
			}
			cfg.DSN += "&search_path=" + name
		} else {
			cfg = fixture.Config(t, db.DriverMySQL)
			if err := database.Raw("SELECT DATABASE()").Scan(&name).Error; err != nil {
				t.Fatal(err)
			}
			cfg.DB = name
		}
		cfgPath := filepath.Join(t.TempDir(), "database.json")
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(cfgPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		task := workerTask(t, database)
		if err = database.Model(&task).
			Updates(map[string]any{"state": "failed", "attempts": 20, "revision": 9, "last_error": "temporary failure"}).
			Error; err != nil {
			t.Fatal(err)
		}
		var before table.BackgroundTask
		if err = database.First(&before, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		args := []string{
			"--db-config",
			cfgPath,
			"--task-id",
			task.ID,
			"--expected-revision",
			"9",
			"--operator",
			"operator-test",
			"--reason",
			"fixed dependency",
			"--record",
			"OPS-CLI-1",
		}
		run := func(execute bool) (bool, error) {
			t.Helper()
			ctx, stop := context.WithTimeout(t.Context(), 15*time.Second)
			defer stop()
			commandArgs := append([]string(nil), args...)
			if execute {
				commandArgs = append(commandArgs, "--execute")
			}
			command := exec.CommandContext(ctx, binary, commandArgs...)
			output, e := command.Output()
			if e != nil {
				return false, e
			}
			var result struct {
				Applied bool
				Before  backgroundtask.RecoveryPreview
			}
			if e = json.Unmarshal(output, &result); e != nil {
				t.Fatal("maintenance stdout is not a valid recovery response")
			}
			if result.Before.TaskID != task.ID || result.Before.Revision != 9 ||
				result.Before.Affected != 1 {
				t.Fatal("maintenance preview does not match selected task")
			}
			return result.Applied, nil
		}
		applied, err := run(false)
		if err != nil || applied {
			t.Fatalf("default dry-run applied=%t err=%v", applied, err)
		}
		var dry table.BackgroundTask
		if err = database.First(&dry, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if dry.State != before.State || dry.Revision != before.Revision ||
			dry.RunGeneration != before.RunGeneration ||
			dry.Attempts != before.Attempts ||
			!bytes.Equal(dry.RecoveryEvents, before.RecoveryEvents) {
			t.Fatal("dry-run changed task")
		}
		applied, err = run(true)
		if err != nil || !applied {
			t.Fatalf("execute applied=%t err=%v", applied, err)
		}
		var after table.BackgroundTask
		if err = database.First(&after, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if after.State != "pending" || after.Revision != 10 || after.RunGeneration != before.RunGeneration+1 ||
			after.Attempts != 0 ||
			after.TotalAttempts != before.TotalAttempts ||
			!after.NotBefore.Equal(before.NotBefore) ||
			!bytes.Equal(after.Payload, before.Payload) {
			t.Fatal("execute did not preserve immutable task state while requeuing")
		}
		var events []backgroundtask.RecoveryEvent
		if err = json.Unmarshal(after.RecoveryEvents, &events); err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Operator != "operator-test" ||
			events[0].Reason != "fixed dependency" ||
			events[0].Record != "OPS-CLI-1" ||
			events[0].PreviousRevision != 9 ||
			events[0].PreviousFailures != 20 ||
			events[0].From != "failed" ||
			events[0].To != "pending" {
			t.Fatal("execute did not persist recovery audit")
		}
		if _, err = run(true); err == nil {
			t.Fatal("repeated revision must exit unsuccessfully")
		}
		var repeated table.BackgroundTask
		if err = database.First(&repeated, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if repeated.Revision != after.Revision || !bytes.Equal(repeated.RecoveryEvents, after.RecoveryEvents) {
			t.Fatal("stale execute changed task")
		}
	})
}
